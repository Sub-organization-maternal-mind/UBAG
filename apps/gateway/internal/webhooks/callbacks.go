package webhooks

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type CallbackConfig struct {
	URL        string
	SecretID   string
	EventTypes map[string]struct{}
}

type URLPolicy struct {
	AllowInsecureHTTP  bool
	AllowPrivateHosts  bool
	AllowAnyPublicHost bool
	AllowedHosts       []string
	Resolver           interface {
		LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
	}
}

func CallbackFromMap(callbacks map[string]any, policy URLPolicy) (CallbackConfig, bool, error) {
	if callbacks == nil {
		return CallbackConfig{}, false, nil
	}
	rawURL, _ := callbacks["webhook_url"].(string)
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return CallbackConfig{}, false, nil
	}
	secretID, _ := callbacks["webhook_secret_id"].(string)
	secretID = strings.TrimSpace(secretID)
	if secretID == "" {
		return CallbackConfig{}, true, fmt.Errorf("callbacks.webhook_secret_id is required when callbacks.webhook_url is set")
	}
	if err := ValidateCallbackURL(context.Background(), rawURL, policy); err != nil {
		return CallbackConfig{}, true, err
	}
	events := map[string]struct{}{}
	if values, ok := callbacks["event_types"].([]any); ok {
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				events[strings.TrimSpace(text)] = struct{}{}
			}
		}
	}
	return CallbackConfig{URL: rawURL, SecretID: secretID, EventTypes: events}, true, nil
}

func ValidateCallbackURL(ctx context.Context, raw string, policy URLPolicy) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.Host == "" {
		return fmt.Errorf("callbacks.webhook_url must be an absolute URL")
	}
	if parsed.User != nil {
		return fmt.Errorf("callbacks.webhook_url must not include userinfo")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("callbacks.webhook_url must not include a fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !policy.AllowInsecureHTTP {
			return fmt.Errorf("callbacks.webhook_url must use https")
		}
	default:
		return fmt.Errorf("callbacks.webhook_url must use http or https")
	}
	if hasSensitiveQuery(parsed.Query()) {
		return fmt.Errorf("callbacks.webhook_url query must not contain secret-like keys")
	}
	host := strings.Trim(strings.ToLower(parsed.Hostname()), "[]")
	if host == "" {
		return fmt.Errorf("callbacks.webhook_url host is required")
	}
	if !policy.AllowAnyPublicHost && !hostAllowed(host, policy.AllowedHosts) {
		return fmt.Errorf("callbacks.webhook_url host is not in the configured allowlist")
	}
	if isBlockedHostName(host) && !policy.AllowPrivateHosts {
		return fmt.Errorf("callbacks.webhook_url must not target loopback, local, or metadata hosts")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateOrLocalIP(ip) && !policy.AllowPrivateHosts {
			return fmt.Errorf("callbacks.webhook_url must not target private or local addresses")
		}
		return nil
	}
	if policy.Resolver != nil {
		addresses, err := resolveHostForPolicy(ctx, host, policy)
		if err != nil {
			return fmt.Errorf("callbacks.webhook_url host could not be resolved: %w", err)
		}
		for _, ip := range addresses {
			if isPrivateOrLocalIP(ip) && !policy.AllowPrivateHosts {
				return fmt.Errorf("callbacks.webhook_url resolves to private or local addresses")
			}
		}
	}
	return nil
}

func resolveHostForPolicy(ctx context.Context, host string, policy URLPolicy) ([]net.IP, error) {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return []net.IP{ip}, nil
	}
	resolver := policy.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("host resolved no addresses")
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		ips = append(ips, address.IP)
	}
	return ips, nil
}

func EventAllowed(config CallbackConfig, eventName string) bool {
	if len(config.EventTypes) == 0 {
		return true
	}
	_, ok := config.EventTypes[eventName]
	return ok
}

func RedactCallbacks(callbacks map[string]any) map[string]any {
	if callbacks == nil {
		return nil
	}
	output := make(map[string]any, len(callbacks))
	for key, value := range callbacks {
		if key == "webhook_url" {
			if text, ok := value.(string); ok {
				output[key] = redactURL(text)
				continue
			}
		}
		output[key] = value
	}
	return output
}

func hasSensitiveQuery(values url.Values) bool {
	for key := range values {
		normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
		for _, marker := range []string{"token", "secret", "apikey", "password", "credential", "cookie"} {
			if strings.Contains(normalized, marker) {
				return true
			}
		}
	}
	return false
}

func hostAllowed(host string, allowed []string) bool {
	if len(allowed) == 0 {
		return false
	}
	for _, allowedHost := range allowed {
		if strings.EqualFold(strings.TrimSpace(allowedHost), host) {
			return true
		}
	}
	return false
}

func isBlockedHostName(host string) bool {
	switch host {
	case "localhost", "metadata.google.internal", "169.254.169.254":
		return true
	}
	return strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local")
}

func isPrivateOrLocalIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified()
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "redacted"
	}
	parsed.User = nil
	if parsed.RawQuery != "" {
		parsed.RawQuery = "redacted=true"
	}
	parsed.Fragment = ""
	return parsed.String()
}
