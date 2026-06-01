package webhooks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/resilience"
)

const defaultMaxResponseBytes = 8 * 1024

type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type HTTPSender struct {
	Client           HTTPDoer
	SecretResolver   SecretResolver
	URLPolicy        URLPolicy
	Now              func() time.Time
	MaxResponseBytes int64
	APIVersion       string
	Breakers         *resilience.Registry // optional; nil disables breaker logic
}

func (s HTTPSender) Send(ctx context.Context, delivery Delivery) (AttemptResult, error) {
	start := time.Now()
	now := s.Now
	if now == nil {
		now = time.Now
	}
	result := AttemptResult{OccurredAt: now().UTC()}
	if err := ValidateCallbackURL(ctx, delivery.URL, s.URLPolicy); err != nil {
		result.ErrorClass = "url_policy"
		result.ErrorMessage = err.Error()
		result.Retryable = false
		return result, nil
	}
	resolver := s.SecretResolver
	if resolver == nil {
		result.ErrorClass = "signing_error"
		result.ErrorMessage = "webhook secret resolver is not configured"
		return result, nil
	}
	secret, found, err := resolver.ResolveWebhookSecret(ctx, delivery.SecretID)
	if err != nil {
		result.ErrorClass = "signing_error"
		result.ErrorMessage = err.Error()
		return result, nil
	}
	if !found {
		result.ErrorClass = "signing_error"
		result.ErrorMessage = "webhook secret was not found"
		return result, nil
	}
	signature, err := SignBody(secret, delivery.Payload, now(), "")
	if err != nil {
		result.ErrorClass = "signing_error"
		result.ErrorMessage = err.Error()
		return result, nil
	}
	// Circuit breaker check (nil-safe: skip entirely when Breakers is not configured).
	var breaker *resilience.Breaker
	if s.Breakers != nil {
		u, err := url.Parse(delivery.URL)
		if err == nil {
			host := u.Hostname() // strips port if present
			breaker = s.Breakers.Get(resilience.KindWebhook, host)
			if !breaker.Allow() {
				result.ErrorClass = "circuit_open"
				result.ErrorMessage = "webhook circuit breaker is open for host: " + host
				result.Retryable = true
				result.StatusCode = 0
				return result, nil
			}
		}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.URL, bytes.NewReader(delivery.Payload))
	if err != nil {
		result.ErrorClass = "request_invalid"
		result.ErrorMessage = err.Error()
		return result, nil
	}
	request.Header.Set("Content-Type", contentTypeJSON)
	request.Header.Set(SignatureHeader, signature.Signature)
	request.Header.Set(TimestampHeader, strconv.FormatInt(signature.Timestamp, 10))
	request.Header.Set(NonceHeader, signature.Nonce)
	request.Header.Set(DeliveryIDHeader, delivery.ID)
	request.Header.Set(WebhookIDHeader, delivery.EndpointID)
	request.Header.Set(JobIDHeader, delivery.JobID)
	request.Header.Set(EventHeader, delivery.EventName)
	request.Header.Set(EventIDHeader, delivery.DedupeKey)
	request.Header.Set(TraceIDHeader, delivery.TraceID)
	if s.APIVersion != "" {
		request.Header.Set(APIVersionHeader, s.APIVersion)
	}
	client := s.Client
	if client == nil {
		client = defaultHTTPClient(10*time.Second, s.URLPolicy)
	}
	response, err := client.Do(request)
	result.Duration = time.Since(start)
	if err != nil {
		result.ErrorClass = "network"
		result.ErrorMessage = sanitizeErrorMessage(err.Error())
		result.Retryable = true
		if breaker != nil {
			breaker.RecordFailure()
		}
		return result, nil
	}
	defer response.Body.Close()
	maxBytes := s.MaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxResponseBytes
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxBytes))
	result.StatusCode = response.StatusCode
	result.Retryable = retryableStatus(response.StatusCode)
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		result.ErrorClass = "none"
		if breaker != nil {
			breaker.RecordSuccess()
		}
		return result, nil
	}
	if breaker != nil && response.StatusCode >= 500 {
		breaker.RecordFailure()
	}
	result.ErrorClass = errorClassForStatus(response.StatusCode)
	result.ErrorMessage = fmt.Sprintf("webhook endpoint returned HTTP %d", response.StatusCode)
	return result, nil
}

func defaultHTTPClient(timeout time.Duration, policy URLPolicy) *http.Client {
	return NewHTTPClient(timeout, policy)
}

func NewHTTPClient(timeout time.Duration, policy URLPolicy) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: safeDialer{Policy: policy}.DialContext,
		},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type safeDialer struct {
	Policy URLPolicy
	Dial   func(ctx context.Context, network string, address string) (net.Conn, error)
}

func (d safeDialer) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := resolveHostForPolicy(ctx, host, d.Policy)
	if err != nil {
		return nil, err
	}
	dial := d.Dial
	if dial == nil {
		netDialer := &net.Dialer{}
		dial = netDialer.DialContext
	}
	var lastErr error
	for _, ip := range addresses {
		if isPrivateOrLocalIP(ip) && !d.Policy.AllowPrivateHosts {
			lastErr = fmt.Errorf("webhook endpoint resolves to private or local address")
			continue
		}
		conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("webhook endpoint resolved no addresses")
}

func retryableStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusConflict ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

func errorClassForStatus(status int) string {
	switch {
	case status >= 500:
		return "http_5xx"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 400:
		return "http_4xx"
	case status >= 300:
		return "redirect"
	default:
		return "unknown"
	}
}

func sanitizeErrorMessage(value string) string {
	value = stringsTrim(value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}
