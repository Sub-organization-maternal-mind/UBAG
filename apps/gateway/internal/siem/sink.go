package siem

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Sink is a destination capable of receiving redacted audit events. Export
// must be safe to call concurrently with itself only insofar as the Exporter
// guarantees: the Exporter serializes Export calls per sink, so a Sink need
// not be internally concurrency-safe, but it must respect ctx cancellation.
type Sink interface {
	// Name returns a stable identifier used in metrics and logs.
	Name() string
	// Export delivers a batch of already-redacted events. A non-nil error
	// signals the Exporter to retry the batch (subject to bounded attempts).
	Export(ctx context.Context, events []Event) error
}

// wireEvent mirrors Event but pins the timestamp to UTC RFC3339 for export.
type wireEvent struct {
	ID         string         `json:"id"`
	TenantID   string         `json:"tenant_id"`
	AppID      string         `json:"app_id"`
	Type       string         `json:"type"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	Outcome    string         `json:"outcome"`
	Timestamp  string         `json:"timestamp"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

func toWire(e Event) wireEvent {
	ts := e.Timestamp.UTC()
	if e.Timestamp.IsZero() {
		ts = time.Time{}
	}
	return wireEvent{
		ID:         e.ID,
		TenantID:   e.TenantID,
		AppID:      e.AppID,
		Type:       e.Type,
		Actor:      e.Actor,
		Action:     e.Action,
		Resource:   e.Resource,
		Outcome:    e.Outcome,
		Timestamp:  ts.Format(time.RFC3339),
		Attributes: e.Attributes,
	}
}

// SecretResolver resolves a secret *reference* identifier into the secret
// material at export time. The reference (never the raw secret) is what is
// persisted in a ConfigStore.
type SecretResolver interface {
	Resolve(ctx context.Context, ref string) (string, bool, error)
}

// StaticSecretResolver is an in-memory resolver, primarily for wiring and
// tests. Keys are reference ids; values are the resolved secrets.
type StaticSecretResolver map[string]string

// Resolve implements SecretResolver.
func (r StaticSecretResolver) Resolve(_ context.Context, ref string) (string, bool, error) {
	value, ok := r[strings.TrimSpace(ref)]
	if !ok || strings.TrimSpace(value) == "" {
		return "", false, nil
	}
	return value, true, nil
}

// FileSink appends newline-delimited JSON events to a file. The parent
// directory is created on demand. Each batch is buffered and written with a
// single Write call under a mutex so concurrent processes appending to the
// same file do not interleave partial lines (atomic-ish; full atomicity
// across processes is not guaranteed by POSIX append for arbitrary sizes, but
// per-batch single-write keeps individual batches coherent).
type FileSink struct {
	Path string

	mu sync.Mutex
}

// NewFileSink constructs a FileSink writing to path.
func NewFileSink(path string) *FileSink {
	return &FileSink{Path: strings.TrimSpace(path)}
}

// Name implements Sink.
func (s *FileSink) Name() string {
	return "file"
}

// Export implements Sink.
func (s *FileSink) Export(_ context.Context, events []Event) error {
	if s == nil || strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("siem: file sink path is required")
	}
	if len(events) == 0 {
		return nil
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, event := range events {
		if err := encoder.Encode(toWire(event)); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir := filepath.Dir(s.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(buf.Bytes()); err != nil {
		return err
	}
	return file.Sync()
}

// HTTPSink POSTs a JSON batch {"events":[...]} to a configured URL. It uses a
// dedicated http.Client whose Transport explicitly disables environment proxy
// resolution (Proxy=nil) and enforces conservative timeouts. A non-2xx
// response returns an error so the Exporter retries the batch.
type HTTPSink struct {
	URL string
	// AuthRef is an optional secret *reference* id. When set together with
	// Resolver, the resolved value is sent as "Authorization: Bearer <value>".
	AuthRef  string
	Resolver SecretResolver
	// Timeout bounds the whole request. Defaults to 10s when zero.
	Timeout time.Duration

	once   sync.Once
	client *http.Client
}

// NewHTTPSink constructs an HTTPSink. resolver and authRef may be empty.
func NewHTTPSink(url string, authRef string, resolver SecretResolver) *HTTPSink {
	return &HTTPSink{URL: strings.TrimSpace(url), AuthRef: strings.TrimSpace(authRef), Resolver: resolver}
}

// Name implements Sink.
func (s *HTTPSink) Name() string {
	return "http"
}

func (s *HTTPSink) httpClient() *http.Client {
	s.once.Do(func() {
		timeout := s.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		transport := &http.Transport{
			// Proxy is explicitly nil: do NOT honor HTTP(S)_PROXY env vars,
			// so audit traffic cannot be silently rerouted.
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
		}
		s.client = &http.Client{Timeout: timeout, Transport: transport}
	})
	return s.client
}

// Export implements Sink.
func (s *HTTPSink) Export(ctx context.Context, events []Event) error {
	if s == nil || strings.TrimSpace(s.URL) == "" {
		return fmt.Errorf("siem: http sink url is required")
	}
	if len(events) == 0 {
		return nil
	}
	wire := make([]wireEvent, len(events))
	for i, event := range events {
		wire[i] = toWire(event)
	}
	body, err := json.Marshal(map[string]any{"events": wire})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if s.Resolver != nil && strings.TrimSpace(s.AuthRef) != "" {
		token, ok, resolveErr := s.Resolver.Resolve(ctx, s.AuthRef)
		if resolveErr != nil {
			return resolveErr
		}
		if ok {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	response, err := s.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("siem: http sink returned status %d", response.StatusCode)
	}
	return nil
}

// SyslogSink writes one RFC5424-ish line per event to a host:port endpoint
// over UDP or TCP. A fresh connection is dialed per Export call to avoid
// holding sockets between bursts; ordering within a batch is preserved by
// writing events sequentially.
type SyslogSink struct {
	// Network is "udp" or "tcp". Defaults to "udp" when empty.
	Network string
	// Address is host:port.
	Address string
	// Hostname is the syslog HOSTNAME field. Defaults to the OS hostname.
	Hostname string
	// AppName is the syslog APP-NAME field. Defaults to "ubag-gateway".
	AppName string
	// Timeout bounds dial + write. Defaults to 5s when zero.
	Timeout time.Duration

	mu sync.Mutex
}

// NewSyslogSink constructs a SyslogSink for the given network/address.
func NewSyslogSink(network string, address string) *SyslogSink {
	return &SyslogSink{Network: strings.TrimSpace(network), Address: strings.TrimSpace(address)}
}

// Name implements Sink.
func (s *SyslogSink) Name() string {
	return "syslog"
}

// Export implements Sink.
func (s *SyslogSink) Export(ctx context.Context, events []Event) error {
	if s == nil || strings.TrimSpace(s.Address) == "" {
		return fmt.Errorf("siem: syslog sink address is required")
	}
	if len(events) == 0 {
		return nil
	}
	network := strings.TrimSpace(s.Network)
	if network == "" {
		network = "udp"
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, network, s.Address)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	writer := bufio.NewWriter(conn)
	for _, event := range events {
		line, err := s.formatLine(event)
		if err != nil {
			return err
		}
		if _, err := writer.WriteString(line); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (s *SyslogSink) formatLine(event Event) (string, error) {
	hostname := strings.TrimSpace(s.Hostname)
	if hostname == "" {
		if h, err := os.Hostname(); err == nil {
			hostname = h
		} else {
			hostname = "-"
		}
	}
	appName := strings.TrimSpace(s.AppName)
	if appName == "" {
		appName = "ubag-gateway"
	}
	// PRI = facility(10=security/auth) * 8 + severity(6=informational) = 86.
	const pri = 86
	ts := event.Timestamp.UTC()
	if event.Timestamp.IsZero() {
		ts = time.Now().UTC()
	}
	structured, err := json.Marshal(toWire(event))
	if err != nil {
		return "", err
	}
	msgID := strings.TrimSpace(event.Type)
	if msgID == "" {
		msgID = "audit"
	}
	procID := strings.TrimSpace(event.TenantID)
	if procID == "" {
		procID = "-"
	}
	// RFC5424: <PRI>VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID SD MSG
	return fmt.Sprintf("<%d>1 %s %s %s %s %s - %s\n",
		pri,
		ts.Format(time.RFC3339),
		sanitizeSyslogField(hostname),
		sanitizeSyslogField(appName),
		sanitizeSyslogField(procID),
		sanitizeSyslogField(msgID),
		structured,
	), nil
}

func sanitizeSyslogField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	value = strings.Map(func(r rune) rune {
		if r == ' ' || r < 0x21 || r > 0x7e {
			return '_'
		}
		return r
	}, value)
	if len(value) > 48 {
		value = value[:48]
	}
	return value
}
