package alerts

import (
	"context"
	"strings"
	"testing"
)

func TestParseRecipientsDefaultsToConfiguredFallback(t *testing.T) {
	if got := parseRecipients(""); len(got) != 1 || got[0] != DefaultRecipient {
		t.Fatalf("empty recipients should default to %q, got %v", DefaultRecipient, got)
	}
	if got := parseRecipients("   ,  , not-an-email , "); len(got) != 1 || got[0] != DefaultRecipient {
		t.Fatalf("invalid-only recipients should default, got %v", got)
	}
	got := parseRecipients("ops@example.com, security@example.com")
	if len(got) != 2 || got[0] != "ops@example.com" || got[1] != "security@example.com" {
		t.Fatalf("unexpected parsed recipients: %v", got)
	}
}

func TestComposeMessageIncludesSubjectAndContext(t *testing.T) {
	alert := Alert{
		AlertID:   "alert_abc",
		TenantID:  "t1",
		JobID:     "job-9",
		SessionID: "sess-1",
		TargetID:  "chatgpt_web",
		Kind:      KindCaptcha,
		Message:   "solve the captcha",
	}
	msg := string(composeMessage("from@example.com", []string{"ops@example.com"}, alert))

	wantSubstrings := []string{
		"Subject: [UBAG] Manual action required: captcha for job job-9",
		"To: ops@example.com",
		"From: from@example.com",
		"job-9",
		"chatgpt_web",
		"sess-1",
		"solve the captcha",
		"alert_abc",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(msg, want) {
			t.Fatalf("composed message missing %q\n---\n%s", want, msg)
		}
	}
}

func TestSMTPEmailSinkUsesDefaultRecipientAndInjectedTransport(t *testing.T) {
	var captured SMTPConfig
	var capturedMessage []byte
	sink := NewSMTPEmailSink(SMTPConfig{Host: "smtp.example.com", Port: 587, From: "from@example.com"}, nil)
	sink.sendMail = func(_ context.Context, cfg SMTPConfig, message []byte) error {
		captured = cfg
		capturedMessage = message
		return nil
	}

	if err := sink.Send(context.Background(), Alert{JobID: "job-1", Kind: KindManualLogin}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(captured.Recipients) != 1 || captured.Recipients[0] != DefaultRecipient {
		t.Fatalf("expected default recipient %q, got %v", DefaultRecipient, captured.Recipients)
	}
	if !strings.Contains(string(capturedMessage), "manual_login") {
		t.Fatalf("message did not carry kind: %s", capturedMessage)
	}
}

func TestSinkFromEnvLogOnlyByDefault(t *testing.T) {
	t.Setenv("UBAG_ALERT_SMTP_HOST", "")
	t.Setenv("UBAG_ALERT_EMAIL_TO", "")
	sink, summary := SinkFromEnv(nil, "memory")
	if _, ok := sink.(*LogSink); !ok {
		t.Fatalf("expected LogSink when SMTP host unset, got %T", sink)
	}
	if summary.SMTPConfigured {
		t.Fatalf("summary should not report SMTP configured")
	}
	if summary.RecipientCount != 1 || summary.Recipients[0] != DefaultRecipient {
		t.Fatalf("expected default recipient in summary, got %+v", summary)
	}
}

func TestSinkFromEnvBuildsSMTPSinkWhenHostSet(t *testing.T) {
	t.Setenv("UBAG_ALERT_SMTP_HOST", "smtp.example.com")
	t.Setenv("UBAG_ALERT_SMTP_PORT", "2525")
	t.Setenv("UBAG_ALERT_SMTP_USERNAME", "mailer@example.com")
	t.Setenv("UBAG_ALERT_SMTP_PASSWORD", "supersecret")
	t.Setenv("UBAG_ALERT_EMAIL_TO", "ops@example.com")
	t.Setenv("UBAG_ALERT_SMTP_TLS", "implicit")

	sink, summary := SinkFromEnv(nil, "postgres")
	if _, ok := sink.(*MultiSink); !ok {
		t.Fatalf("expected MultiSink when SMTP host set, got %T", sink)
	}
	if !summary.SMTPConfigured || summary.SMTPHost != "smtp.example.com" {
		t.Fatalf("summary should report SMTP host, got %+v", summary)
	}
	if summary.StoreKind != "postgres" {
		t.Fatalf("summary store kind = %q", summary.StoreKind)
	}
	// The secret-free summary must never leak the password.
	if strings.Contains(summary.SinkType, "supersecret") || containsRecipient(summary.Recipients, "supersecret") {
		t.Fatalf("summary leaked secret material: %+v", summary)
	}
}

func containsRecipient(recipients []string, needle string) bool {
	for _, r := range recipients {
		if strings.Contains(r, needle) {
			return true
		}
	}
	return false
}
