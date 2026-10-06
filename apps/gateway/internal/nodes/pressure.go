package nodes

import "time"

const (
	pressureCPUHigh = 80.0            // percent; above this admission is reduced
	pressureMemLow  = 20.0            // percent available; below this admission is reduced
	recoverCPULow   = 60.0            // recovery needs CPU below this...
	recoverMemHigh  = 25.0            // ...and available memory above this...
	recoverHold     = 2 * time.Minute // ...continuously for this long
)

// PressureSample is one helper resource report.
type PressureSample struct {
	CPUPercent      float64
	MemAvailPercent float64
}

// Pressure is the hysteresis state. The zero value is "normal".
type Pressure struct {
	Reduced   bool
	calmSince time.Time // zero unless a recovery window is open
}

// Step advances the state with one sample at time now. Entering is immediate;
// leaving needs recoverHold of continuous calm, and any non-calm sample (even
// the dead band between the thresholds) restarts that window.
func (p Pressure) Step(s PressureSample, now time.Time) Pressure {
	if s.CPUPercent > pressureCPUHigh || s.MemAvailPercent < pressureMemLow {
		return Pressure{Reduced: true}
	}
	if !p.Reduced {
		return Pressure{}
	}
	if s.CPUPercent >= recoverCPULow || s.MemAvailPercent <= recoverMemHigh {
		return Pressure{Reduced: true}
	}
	if p.calmSince.IsZero() {
		p.calmSince = now
	}
	if now.Sub(p.calmSince) >= recoverHold {
		return Pressure{}
	}
	return p
}

// AdmissionLimit halves the workload limit under pressure (never below 1, so a
// reduced helper still drains and reports); unchanged when normal.
// ponytail: fixed halving, make it a ramp if the lab shows oscillation.
func (p Pressure) AdmissionLimit(limit int) int {
	if !p.Reduced || limit <= 1 {
		return limit
	}
	return max(1, limit/2)
}
