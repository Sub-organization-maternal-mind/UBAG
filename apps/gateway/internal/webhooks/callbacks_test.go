package webhooks

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestCallbackFromMapValidatesURLAndSecretReference(t *testing.T) {
	callback, ok, err := CallbackFromMap(map[string]any{
		"webhook_url":       "https://example.com/ubag/callback",
		"webhook_secret_id": "wh_sec_test",
		"event_types":       []any{"job.completed"},
	}, URLPolicy{AllowedHosts: []string{"example.com"}})
	if err != nil || !ok {
		t.Fatalf("CallbackFromMap ok=%v err=%v", ok, err)
	}
	if callback.SecretID != "wh_sec_test" || !EventAllowed(callback, "job.completed") || EventAllowed(callback, "job.failed") {
		t.Fatalf("unexpected callback config: %#v", callback)
	}
}

func TestCallbackFromMapRejectsUnsafeURLs(t *testing.T) {
	tests := []string{
		"http://example.com/callback",
		"https://127.0.0.1/callback",
		"https://169.254.169.254/latest/meta-data",
		"https://user:pass@example.com/callback",
		"https://example.com/callback?token=secret",
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, _, err := CallbackFromMap(map[string]any{
				"webhook_url":       raw,
				"webhook_secret_id": "wh_sec_test",
			}, URLPolicy{AllowedHosts: []string{"example.com"}})
			if err == nil {
				t.Fatalf("CallbackFromMap accepted unsafe URL %q", raw)
			}
		})
	}
}

func TestValidateCallbackURLRequiresAllowlistOrExplicitAnyPublicHost(t *testing.T) {
	if err := ValidateCallbackURL(context.Background(), "https://example.com/callback", URLPolicy{}); err == nil {
		t.Fatal("ValidateCallbackURL accepted a public host without allowlist or explicit allow-any setting")
	}
	if err := ValidateCallbackURL(context.Background(), "https://example.com/callback", URLPolicy{AllowAnyPublicHost: true}); err != nil {
		t.Fatalf("ValidateCallbackURL rejected explicit allow-any public host: %v", err)
	}
}

func TestValidateCallbackURLRejectsResolvedPrivateAddresses(t *testing.T) {
	policy := URLPolicy{Resolver: staticResolver{
		"public-name.test": {net.ParseIP("127.0.0.1"), net.ParseIP("10.0.0.8"), net.ParseIP("169.254.169.254"), net.ParseIP("::1")},
	}, AllowedHosts: []string{"public-name.test"}}
	if err := ValidateCallbackURL(context.Background(), "https://public-name.test/callback", policy); err == nil {
		t.Fatal("ValidateCallbackURL accepted a hostname resolving to private/local addresses")
	}
}

func TestSafeDialerRechecksResolvedAddressAtConnectTime(t *testing.T) {
	dialer := safeDialer{
		Policy: URLPolicy{Resolver: staticResolver{
			"public-name.test": {net.ParseIP("169.254.169.254")},
		}},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dial should not be called for private resolved webhook address")
			return nil, nil
		},
	}
	if _, err := dialer.DialContext(context.Background(), "tcp", "public-name.test:443"); err == nil {
		t.Fatal("safeDialer accepted private resolved webhook address")
	}
}

func TestSafeDialerUsesResolvedPublicAddress(t *testing.T) {
	var dialed string
	dialer := safeDialer{
		Policy: URLPolicy{Resolver: staticResolver{
			"public-name.test": {net.ParseIP("203.0.113.10")},
		}},
		Dial: func(_ context.Context, _ string, address string) (net.Conn, error) {
			dialed = address
			return nil, context.Canceled
		},
	}
	if _, err := dialer.DialContext(context.Background(), "tcp", "public-name.test:443"); err == nil {
		t.Fatal("safeDialer unexpectedly succeeded with fake dial")
	}
	if dialed != "203.0.113.10:443" {
		t.Fatalf("safeDialer dialed %q", dialed)
	}
}

func TestNewHTTPClientDisablesEnvironmentProxy(t *testing.T) {
	client := NewHTTPClient(0, URLPolicy{})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("webhook HTTP client must not use environment proxies")
	}
}

func TestRedactCallbacksRemovesSensitiveURLParts(t *testing.T) {
	redacted := RedactCallbacks(map[string]any{
		"webhook_url":       "https://example.com/callback?token=secret",
		"webhook_secret_id": "wh_sec_test",
	})
	if redacted["webhook_url"] == "https://example.com/callback?token=secret" {
		t.Fatalf("webhook URL query was not redacted: %#v", redacted)
	}
}

type staticResolver map[string][]net.IP

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips := r[host]
	addresses := make([]net.IPAddr, 0, len(ips))
	for _, ip := range ips {
		addresses = append(addresses, net.IPAddr{IP: ip})
	}
	return addresses, nil
}
