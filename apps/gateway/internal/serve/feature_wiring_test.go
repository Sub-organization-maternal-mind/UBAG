package serve

import (
	"context"
	"testing"
)

// TestConversationsDisabledByDefault pins the inert-by-default gate: without
// the flag, no conversation manager is wired (the /v1/conversations route
// stays 501 and no conversation block is injected).
func TestConversationsDisabledByDefault(t *testing.T) {
	t.Setenv("UBAG_CONVERSATIONS_ENABLED", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.conversations != nil {
		t.Fatalf("conversations manager should be nil when disabled, got %T", out.conversations)
	}
}

// TestConversationsEnabledMemory wires a manager on the memory store kind.
func TestConversationsEnabledMemory(t *testing.T) {
	t.Setenv("UBAG_CONVERSATIONS_ENABLED", "true")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.conversations == nil {
		t.Fatal("conversations manager should be wired when enabled")
	}
}

// TestRateLimitFlagAndDefault covers the rate-limit gate: off by default,
// on with an explicit flag, with a usable limiter in both cases.
func TestRateLimitFlagAndDefault(t *testing.T) {
	t.Setenv("UBAG_RATE_LIMIT_ENABLED", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.rateLimitEnabled {
		t.Fatal("rate limiting should be disabled by default")
	}

	t.Setenv("UBAG_RATE_LIMIT_ENABLED", "true")
	out, err = newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if !out.rateLimitEnabled {
		t.Fatal("rate limiting should be enabled with the flag set")
	}
	if out.rateLimiter == nil {
		t.Fatal("rate limiter should always be wired")
	}
}

// TestCacheTTLParsing covers the cache tuning knob: default TTL when unset,
// custom TTL from env, and a hard error on garbage (mirrors the PAT TTL
// recipe).
func TestCacheTTLParsing(t *testing.T) {
	t.Setenv("UBAG_CACHE_TTL_MS", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.responseCache == nil {
		t.Fatal("response cache should always be wired")
	}

	t.Setenv("UBAG_CACHE_TTL_MS", "60000") // 1m
	if _, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil); err != nil {
		t.Fatalf("valid TTL rejected: %v", err)
	}

	t.Setenv("UBAG_CACHE_TTL_MS", "bogus")
	if _, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil); err == nil {
		t.Fatal("invalid TTL accepted; want a hard error")
	}
}
