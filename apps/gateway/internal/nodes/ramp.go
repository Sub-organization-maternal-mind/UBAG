package nodes

// RampSamples is how many consecutive healthy, saturated samples earn one more
// workload (8 x the 15 s heartbeat = 2 min per step).
const RampSamples = 8

// Ramp is the 1-to-N ramp state: the earned workload limit and the run of
// qualifying samples toward the next step. The zero value is a new helper.
// The caller owns and persists it (HelperState.RampedLimit carries Limit).
type Ramp struct {
	Limit int
	Good  int
}

// Step feeds one measured sample. A step up (+1, never a jump) needs
// RampSamples consecutive samples that are healthy (CPU < 60%, available
// memory > 25%, pressure not reduced) while the helper ran at its current limit
// (active >= limit), so the measurement reflects that load. Any unhealthy or
// unsaturated sample restarts the run. maxN is the grant/ceiling bound; the
// earned limit is clamped to it. Pressure itself is handled by AdmissionLimit,
// not by lowering the earned limit.
// ponytail: no step-down here, add one if the lab shows a ramped limit that
// keeps re-entering pressure.
func (r Ramp) Step(s PressureSample, p Pressure, active, maxN int) Ramp {
	if r.Limit <= 0 {
		r.Limit = NewHelperWorkloads
	}
	if maxN < 1 { // no capacity to ramp into; hold
		return Ramp{Limit: r.Limit}
	}
	r.Limit = min(r.Limit, maxN)
	healthy := !p.Reduced && s.CPUPercent < recoverCPULow && s.MemAvailPercent > recoverMemHigh
	if !healthy || active < r.Limit {
		return Ramp{Limit: r.Limit}
	}
	r.Good++
	if r.Good >= RampSamples {
		if r.Limit < maxN {
			r.Limit++
		}
		r.Good = 0
	}
	return r
}
