package alerts

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"
)

// SMTP TLS modes.
const (
	TLSModeStartTLS = "starttls"
	TLSModeImplicit = "implicit"
	TLSModeNone     = "none"
)

// SMTPConfig describes an outbound email transport for alert notifications.
// The Password is read only from the environment and is never logged or
// returned by any API surface.
type SMTPConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	Recipients []string
	TLSMode    string
}

// SMTPEmailSink delivers alerts as email via net/smtp.
type SMTPEmailSink struct {
	config      SMTPConfig
	logger      *slog.Logger
	dialTimeout time.Duration

	// sendMail is injectable so tests can assert on the composed message and
	// recipients without contacting a real SMTP server.
	sendMail func(ctx context.Context, cfg SMTPConfig, message []byte) error
}

// NewSMTPEmailSink constructs an SMTPEmailSink. A nil logger falls back to
// slog.Default.
func NewSMTPEmailSink(config SMTPConfig, logger *slog.Logger) *SMTPEmailSink {
	if logger == nil {
		logger = slog.Default()
	}
	sink := &SMTPEmailSink{
		config:      config,
		logger:      logger,
		dialTimeout: 10 * time.Second,
	}
	sink.sendMail = sink.deliver
	return sink
}

func (s *SMTPEmailSink) Send(ctx context.Context, alert Alert) error {
	recipients := s.config.Recipients
	if len(recipients) == 0 {
		recipients = []string{DefaultRecipient}
	}
	message := composeMessage(s.config.From, recipients, alert)
	cfg := s.config
	cfg.Recipients = recipients
	return s.sendMail(ctx, cfg, message)
}

// composeMessage builds the RFC 5322 message bytes for an alert email.
func composeMessage(from string, recipients []string, alert Alert) []byte {
	if strings.TrimSpace(from) == "" {
		from = DefaultRecipient
	}
	subject := fmt.Sprintf("[UBAG] Manual action required: %s for job %s", alert.Kind, alert.JobID)

	var body strings.Builder
	body.WriteString("A worker reported that a job needs a manual human action.\r\n")
	body.WriteString("Please open the live browser session and solve it so the flow can resume.\r\n\r\n")
	fmt.Fprintf(&body, "Kind:       %s\r\n", alert.Kind)
	fmt.Fprintf(&body, "Job ID:     %s\r\n", alert.JobID)
	fmt.Fprintf(&body, "Tenant:     %s\r\n", alert.TenantID)
	if alert.AppID != "" {
		fmt.Fprintf(&body, "App:        %s\r\n", alert.AppID)
	}
	if alert.TargetID != "" {
		fmt.Fprintf(&body, "Target:     %s\r\n", alert.TargetID)
	}
	if alert.SessionID != "" {
		fmt.Fprintf(&body, "Session:    %s\r\n", alert.SessionID)
	}
	if alert.Message != "" {
		fmt.Fprintf(&body, "Details:    %s\r\n", alert.Message)
	}
	fmt.Fprintf(&body, "Alert ID:   %s\r\n", alert.AlertID)
	body.WriteString("\r\nThis is the ToS-safe design: a human solves the challenge, never the machine.\r\n")

	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body.String())
	return []byte(msg.String())
}

// deliver sends message to cfg.Recipients honouring the configured TLS mode.
func (s *SMTPEmailSink) deliver(ctx context.Context, cfg SMTPConfig, message []byte) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return fmt.Errorf("alerts: smtp host is not configured")
	}
	from := cfg.From
	if strings.TrimSpace(from) == "" {
		from = DefaultRecipient
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: s.dialTimeout}

	var client *smtp.Client
	switch strings.ToLower(strings.TrimSpace(cfg.TLSMode)) {
	case TLSModeImplicit:
		conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return fmt.Errorf("alerts: smtp tls dial: %w", err)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("alerts: smtp client: %w", err)
		}
	default:
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("alerts: smtp dial: %w", err)
		}
		client, err = smtp.NewClient(conn, cfg.Host)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("alerts: smtp client: %w", err)
		}
		if strings.ToLower(strings.TrimSpace(cfg.TLSMode)) != TLSModeNone {
			if ok, _ := client.Extension("STARTTLS"); ok {
				if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
					_ = client.Close()
					return fmt.Errorf("alerts: smtp starttls: %w", err)
				}
			}
		}
	}
	defer func() { _ = client.Close() }()

	if cfg.Username != "" && cfg.Password != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("alerts: smtp auth: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("alerts: smtp mail from: %w", err)
	}
	for _, rcpt := range cfg.Recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("alerts: smtp rcpt %q: %w", rcpt, err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("alerts: smtp data: %w", err)
	}
	if _, err := wc.Write(message); err != nil {
		_ = wc.Close()
		return fmt.Errorf("alerts: smtp write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("alerts: smtp close: %w", err)
	}
	return client.Quit()
}

// ---------------------------------------------------------------------------
// Env-driven construction
// ---------------------------------------------------------------------------

// parseRecipients splits a comma-separated recipient list, validates that each
// entry looks like an email address, and falls back to DefaultRecipient when
// none are configured.
func parseRecipients(raw string) []string {
	out := make([]string, 0, 4)
	for _, part := range strings.Split(raw, ",") {
		addr := strings.TrimSpace(part)
		if addr == "" {
			continue
		}
		if at := strings.IndexByte(addr, '@'); at <= 0 || at == len(addr)-1 {
			continue // skip values that are not plausible email addresses
		}
		out = append(out, addr)
	}
	if len(out) == 0 {
		return []string{DefaultRecipient}
	}
	return out
}

func smtpPortFromEnv(value string, fallback int) int {
	if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed > 0 {
		return parsed
	}
	return fallback
}

func tlsModeFromEnv(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case TLSModeImplicit:
		return TLSModeImplicit
	case TLSModeNone:
		return TLSModeNone
	default:
		return TLSModeStartTLS
	}
}

// SinkFromEnv builds an AlertSink and its secret-free ConfigSummary from the
// environment. When UBAG_ALERT_SMTP_HOST is set, alerts go to both a LogSink
// and an SMTPEmailSink; otherwise only the LogSink is used. storeKind is echoed
// into the summary for observability.
//
// Recognised variables:
//
//	UBAG_ALERT_SMTP_HOST      SMTP server host (enables email when set)
//	UBAG_ALERT_SMTP_PORT      SMTP port (default 587)
//	UBAG_ALERT_SMTP_USERNAME  SMTP auth username
//	UBAG_ALERT_SMTP_PASSWORD  SMTP auth password (never logged or returned)
//	UBAG_ALERT_SMTP_FROM      envelope/From address (default username or recipient)
//	UBAG_ALERT_SMTP_TLS       starttls (default) | implicit | none
//	UBAG_ALERT_EMAIL_TO       comma-separated recipients (default mindreader420123@gmail.com)
func SinkFromEnv(logger *slog.Logger, storeKind string) (AlertSink, ConfigSummary) {
	if logger == nil {
		logger = slog.Default()
	}
	recipients := parseRecipients(os.Getenv("UBAG_ALERT_EMAIL_TO"))
	logSink := NewLogSink(logger)
	host := strings.TrimSpace(os.Getenv("UBAG_ALERT_SMTP_HOST"))
	if host == "" {
		return logSink, ConfigSummary{
			SinkType:       "log",
			StoreKind:      storeKind,
			RecipientCount: len(recipients),
			Recipients:     recipients,
		}
	}

	from := strings.TrimSpace(os.Getenv("UBAG_ALERT_SMTP_FROM"))
	username := strings.TrimSpace(os.Getenv("UBAG_ALERT_SMTP_USERNAME"))
	if from == "" {
		if strings.Contains(username, "@") {
			from = username
		} else {
			from = recipients[0]
		}
	}
	cfg := SMTPConfig{
		Host:       host,
		Port:       smtpPortFromEnv(os.Getenv("UBAG_ALERT_SMTP_PORT"), 587),
		Username:   username,
		Password:   os.Getenv("UBAG_ALERT_SMTP_PASSWORD"),
		From:       from,
		Recipients: recipients,
		TLSMode:    tlsModeFromEnv(os.Getenv("UBAG_ALERT_SMTP_TLS")),
	}
	smtpSink := NewSMTPEmailSink(cfg, logger)
	return NewMultiSink(logSink, smtpSink), ConfigSummary{
		SinkType:       "smtp+log",
		SMTPConfigured: true,
		SMTPHost:       host,
		StoreKind:      storeKind,
		RecipientCount: len(recipients),
		Recipients:     recipients,
	}
}
