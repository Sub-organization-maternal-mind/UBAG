// Package obs — SLO burn-rate math (§18 / Task 2.6).
//
// FailureBudget tracks a rolling-window failure budget for a single SLO.
// It computes the burn rate (how fast the budget is being consumed relative
// to the allowed rate) using a sliding window of recorded outcomes.
package obs

import (
	"sync"
	"time"
)

// SLOConfig defines an availability SLO.
type SLOConfig struct {
	// Target is the availability objective (e.g. 0.999 = 99.9%).
	Target float64
	// Window is the rolling observation window (e.g. 30*24*time.Hour for 30 days).
	Window time.Duration
}

// SLOOutcome is a single recorded observation.
type SLOOutcome struct {
	At      time.Time
	Success bool
}

// FailureBudget tracks SLO compliance over a rolling window.
type FailureBudget struct {
	mu       sync.Mutex
	cfg      SLOConfig
	outcomes []SLOOutcome

	// consecutiveFailures counts the current run of failures (for health flip).
	consecutiveFailures int
}

// NewFailureBudget creates a new failure budget tracker for the given SLO.
func NewFailureBudget(cfg SLOConfig) *FailureBudget {
	return &FailureBudget{cfg: cfg}
}

// Record adds an outcome to the rolling window.
func (fb *FailureBudget) Record(at time.Time, success bool) {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	fb.outcomes = append(fb.outcomes, SLOOutcome{At: at, Success: success})
	fb.trim(at)

	if success {
		fb.consecutiveFailures = 0
	} else {
		fb.consecutiveFailures++
	}
}

// trim removes outcomes that have fallen outside the rolling window.
func (fb *FailureBudget) trim(now time.Time) {
	cutoff := now.Add(-fb.cfg.Window)
	i := 0
	for i < len(fb.outcomes) && fb.outcomes[i].At.Before(cutoff) {
		i++
	}
	fb.outcomes = fb.outcomes[i:]
}

// Stats returns a snapshot of current SLO statistics.
func (fb *FailureBudget) Stats(now time.Time) SLOStats {
	fb.mu.Lock()
	defer fb.mu.Unlock()

	fb.trim(now)

	total := len(fb.outcomes)
	failures := 0
	for _, o := range fb.outcomes {
		if !o.Success {
			failures++
		}
	}

	var errorRate float64
	if total > 0 {
		errorRate = float64(failures) / float64(total)
	}

	// Allowed error rate: 1 - target (e.g. 0.001 for 99.9% target).
	allowedErrorRate := 1.0 - fb.cfg.Target

	// Burn rate: how many times faster than the allowed rate we are burning.
	// > 1 means budget is burning faster than it can be replenished.
	var burnRate float64
	if allowedErrorRate > 0 {
		burnRate = errorRate / allowedErrorRate
	}

	// Remaining budget fraction: fraction of the allowed errors not yet consumed.
	var remainingFraction float64
	if total > 0 && allowedErrorRate > 0 {
		allowedFailures := allowedErrorRate * float64(total)
		remainingFraction = 1.0 - (float64(failures) / allowedFailures)
		if remainingFraction < 0 {
			remainingFraction = 0
		}
	} else {
		remainingFraction = 1.0
	}

	return SLOStats{
		Total:               total,
		Failures:            failures,
		ErrorRate:           errorRate,
		BurnRate:            burnRate,
		RemainingFraction:   remainingFraction,
		ConsecutiveFailures: fb.consecutiveFailures,
		Healthy:             fb.consecutiveFailures < 3, // flip after N consecutive failures
	}
}

// Healthy reports whether the service is currently healthy (fewer than
// healthThreshold consecutive failures).
func (fb *FailureBudget) Healthy(healthThreshold int) bool {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return fb.consecutiveFailures < healthThreshold
}

// SLOStats is a point-in-time snapshot.
type SLOStats struct {
	Total               int
	Failures            int
	ErrorRate           float64
	BurnRate            float64 // > 1 means burning faster than allowed
	RemainingFraction   float64 // 0..1; 0 = budget exhausted
	ConsecutiveFailures int
	Healthy             bool
}

// PrometheusLines returns Prometheus text-format metrics for this SLO.
func (s SLOStats) PrometheusLines(sloName string) string {
	return "ubag_synthetic_slo_burn_rate{slo=\"" + sloName + "\"} " + formatFloat(s.BurnRate) + "\n" +
		"ubag_synthetic_slo_error_rate{slo=\"" + sloName + "\"} " + formatFloat(s.ErrorRate) + "\n" +
		"ubag_synthetic_slo_remaining_fraction{slo=\"" + sloName + "\"} " + formatFloat(s.RemainingFraction) + "\n"
}

func formatFloat(f float64) string {
	return strconv(f)
}

func strconv(f float64) string {
	// Use fmt to format; avoid importing fmt in the interface — use a simple approach.
	if f == 0 {
		return "0"
	}
	// Round to 6 decimal places.
	s := make([]byte, 0, 20)
	s = appendFloat(s, f)
	return string(s)
}

func appendFloat(buf []byte, f float64) []byte {
	// Simple float formatter sufficient for metric values.
	// Uses integer math to avoid importing strconv or fmt.
	negative := false
	if f < 0 {
		negative = true
		f = -f
	}
	intPart := int64(f)
	fracPart := f - float64(intPart)

	if negative {
		buf = append(buf, '-')
	}
	buf = appendInt(buf, intPart)
	buf = append(buf, '.')
	// 6 decimal places.
	for i := 0; i < 6; i++ {
		fracPart *= 10
		digit := int64(fracPart)
		buf = append(buf, byte('0'+digit))
		fracPart -= float64(digit)
	}
	return buf
}

func appendInt(buf []byte, n int64) []byte {
	if n == 0 {
		return append(buf, '0')
	}
	var tmp [20]byte
	i := len(tmp)
	for n > 0 {
		i--
		tmp[i] = byte('0' + n%10)
		n /= 10
	}
	return append(buf, tmp[i:]...)
}
