package obs_test

import (
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/obs"
)

var (
	cfg30d = obs.SLOConfig{
		Target: 0.999, // 99.9% availability
		Window: 30 * 24 * time.Hour,
	}
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

// TestBurnRateZeroWithPerfectHistory verifies burn rate is 0 when all requests succeed.
func TestBurnRateZeroWithPerfectHistory(t *testing.T) {
	fb := obs.NewFailureBudget(cfg30d)
	for i := 0; i < 1000; i++ {
		fb.Record(t0.Add(time.Duration(i)*time.Minute), true)
	}
	stats := fb.Stats(t0.Add(1001 * time.Minute))
	if stats.BurnRate != 0 {
		t.Errorf("expected burn rate 0 with all successes, got %.6f", stats.BurnRate)
	}
	if !stats.Healthy {
		t.Error("expected healthy with all successes")
	}
}

// TestBurnRateCorrectMath verifies burn rate math for a known error rate.
func TestBurnRateCorrectMath(t *testing.T) {
	fb := obs.NewFailureBudget(cfg30d)
	// Record 100 outcomes: 2 failures, 98 successes → 2% error rate.
	// Allowed error rate = 1 - 0.999 = 0.1%.
	// Burn rate = 2% / 0.1% = 20.
	for i := 0; i < 98; i++ {
		fb.Record(t0.Add(time.Duration(i)*time.Minute), true)
	}
	fb.Record(t0.Add(98*time.Minute), false)
	fb.Record(t0.Add(99*time.Minute), false)

	stats := fb.Stats(t0.Add(100 * time.Minute))

	if stats.Total != 100 {
		t.Errorf("expected 100 total, got %d", stats.Total)
	}
	if stats.Failures != 2 {
		t.Errorf("expected 2 failures, got %d", stats.Failures)
	}
	// Burn rate should be close to 20.
	if stats.BurnRate < 18 || stats.BurnRate > 22 {
		t.Errorf("expected burn rate ~20, got %.4f", stats.BurnRate)
	}
	// Budget is massively over-burned.
	if stats.RemainingFraction > 0 {
		t.Errorf("expected 0 remaining budget (exhausted), got %.4f", stats.RemainingFraction)
	}
}

// TestHealthFlipsAfterConsecutiveFailures verifies the health signal.
func TestHealthFlipsAfterConsecutiveFailures(t *testing.T) {
	fb := obs.NewFailureBudget(cfg30d)

	// Initially healthy.
	fb.Record(t0, true)
	if !fb.Healthy(3) {
		t.Error("expected healthy after first success")
	}

	// Two consecutive failures — still under threshold of 3.
	fb.Record(t0.Add(time.Minute), false)
	fb.Record(t0.Add(2*time.Minute), false)
	if !fb.Healthy(3) {
		t.Error("expected healthy with 2 consecutive failures (threshold=3)")
	}

	// Third consecutive failure — health flips.
	fb.Record(t0.Add(3*time.Minute), false)
	if fb.Healthy(3) {
		t.Error("expected unhealthy after 3 consecutive failures")
	}

	// Recovery: a success resets the consecutive counter.
	fb.Record(t0.Add(4*time.Minute), true)
	if !fb.Healthy(3) {
		t.Error("expected healthy after recovery success")
	}
}

// TestRollingWindowEvictsOldOutcomes verifies that outcomes outside the window
// are dropped and don't affect the burn rate calculation.
func TestRollingWindowEvictsOldOutcomes(t *testing.T) {
	fb := obs.NewFailureBudget(obs.SLOConfig{
		Target: 0.99,
		Window: time.Hour,
	})

	// Record 10 failures 2 hours ago — these should be evicted.
	for i := 0; i < 10; i++ {
		fb.Record(t0.Add(-2*time.Hour+time.Duration(i)*time.Minute), false)
	}
	// Record 100 successes in the current window.
	for i := 0; i < 100; i++ {
		fb.Record(t0.Add(time.Duration(i)*time.Second), true)
	}

	stats := fb.Stats(t0.Add(100 * time.Second))
	if stats.Failures != 0 {
		t.Errorf("expected 0 failures (old ones evicted), got %d", stats.Failures)
	}
	if stats.BurnRate != 0 {
		t.Errorf("expected 0 burn rate after eviction, got %.4f", stats.BurnRate)
	}
}

// TestPrometheusLinesFormat verifies the metric output format is valid.
func TestPrometheusLinesFormat(t *testing.T) {
	fb := obs.NewFailureBudget(cfg30d)
	fb.Record(t0, true)
	stats := fb.Stats(t0.Add(time.Second))

	lines := stats.PrometheusLines("gateway-availability")
	if lines == "" {
		t.Fatal("expected non-empty prometheus lines")
	}
	for _, required := range []string{
		"ubag_synthetic_slo_burn_rate",
		"ubag_synthetic_slo_error_rate",
		"ubag_synthetic_slo_remaining_fraction",
	} {
		if !containsStr(lines, required) {
			t.Errorf("expected metric %q in output", required)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s[1:], sub) || s[:len(sub)] == sub)
}
