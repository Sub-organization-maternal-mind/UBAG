package nodes

import (
	"testing"
	"time"
)

func TestShrinkNeverBelowUsage(t *testing.T) {
	for _, c := range []struct {
		target, active, wantCap, wantFree int
	}{
		{4, 1, 4, 3},
		{2, 4, 4, 0}, // shrink under load: cap held at usage, nothing new admitted
		{2, 2, 2, 0},
		{2, 1, 2, 1},
		{-1, 0, 0, 0},
		{0, 3, 3, 0},
	} {
		cp, fr := ShrinkCap(c.target, Usage{Attempts: c.active})
		if cp != c.wantCap || fr != c.wantFree {
			t.Errorf("ShrinkCap(%d,%d) = %d/%d want %d/%d", c.target, c.active, cp, fr, c.wantCap, c.wantFree)
		}
	}
}

func TestPlanDrain(t *testing.T) {
	grace := time.Minute
	start := t0
	cases := []struct {
		name  string
		state string
		u     Usage
		start time.Time
		at    time.Duration
		grace time.Duration
		want  DrainPlan
	}{
		{"active is no-op", StateActive, Usage{Attempts: 2}, start, time.Hour, grace, DrainPlan{}},
		{"unknown state is no-op", "?", Usage{Attempts: 2}, start, time.Hour, grace, DrainPlan{}},
		{"within grace finishes in flight", StateDraining, Usage{Attempts: 2}, start, 59 * time.Second, grace,
			DrainPlan{CallDrain: true, GraceSeconds: 60}},
		{"after grace cancels non-voice", StateDraining, Usage{Attempts: 2}, start, time.Minute, grace,
			DrainPlan{CallDrain: true, GraceSeconds: 60, CancelNonVoice: true}},
		{"voice only is never cancelled", StateDraining, Usage{Attempts: 2, Voice: 2}, start, time.Hour, grace,
			DrainPlan{CallDrain: true, GraceSeconds: 60, VoiceHeld: 2}},
		{"mixed cancels non-voice, holds voice", StateRevoked, Usage{Attempts: 3, Voice: 1}, start, 2 * time.Minute, grace,
			DrainPlan{CallDrain: true, GraceSeconds: 60, CancelNonVoice: true, VoiceHeld: 1}},
		{"unknown drain start never cancels", StateDraining, Usage{Attempts: 2}, time.Time{}, time.Hour, grace,
			DrainPlan{CallDrain: true, GraceSeconds: 60}},
		{"idle is done", StateDraining, Usage{}, start, 0, grace, DrainPlan{CallDrain: true, GraceSeconds: 60, Done: true}},
		{"zero grace uses default", StateDraining, Usage{Attempts: 1}, start, time.Minute, 0,
			DrainPlan{CallDrain: true, GraceSeconds: 300}},
		{"grace capped", StateDraining, Usage{Attempts: 1}, start, 0, 48 * time.Hour,
			DrainPlan{CallDrain: true, GraceSeconds: 3600}},
		{"voice above attempts is clamped", StateDraining, Usage{Attempts: 1, Voice: 5}, start, time.Hour, grace,
			DrainPlan{CallDrain: true, GraceSeconds: 60, VoiceHeld: 1}},
	}
	for _, c := range cases {
		if got := PlanDrain(c.state, c.u, c.start, t0.Add(c.at), c.grace); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestRampOneToN(t *testing.T) {
	good := PressureSample{CPUPercent: 30, MemAvailPercent: 50}
	var r Ramp
	r = r.Step(good, Pressure{}, 0, 3) // idle: normalises to 1, no progress
	if r.Limit != 1 || r.Good != 0 {
		t.Fatalf("new helper: %+v", r)
	}
	for i := 0; i < RampSamples-1; i++ {
		r = r.Step(good, Pressure{}, 1, 3)
	}
	if r.Limit != 1 {
		t.Fatalf("stepped early: %+v", r)
	}
	r = r.Step(good, Pressure{}, 1, 3)
	if r.Limit != 2 || r.Good != 0 {
		t.Fatalf("want 2: %+v", r)
	}
	// unsaturated sample resets the run
	r.Good = RampSamples - 1
	if r = r.Step(good, Pressure{}, 1, 3); r.Good != 0 || r.Limit != 2 {
		t.Fatalf("unsaturated must reset: %+v", r)
	}
	// hot, dead-band, low-memory and reduced-pressure samples reset
	for _, bad := range []struct {
		s PressureSample
		p Pressure
	}{
		{PressureSample{CPUPercent: 60, MemAvailPercent: 50}, Pressure{}},
		{PressureSample{CPUPercent: 30, MemAvailPercent: 25}, Pressure{}},
		{PressureSample{CPUPercent: 30, MemAvailPercent: 50}, Pressure{Reduced: true}},
	} {
		x := Ramp{Limit: 2, Good: RampSamples - 1}.Step(bad.s, bad.p, 2, 3)
		if x.Good != 0 || x.Limit != 2 {
			t.Errorf("bad sample %+v must reset without stepping: %+v", bad, x)
		}
	}
	// never exceeds maxN, and a shrunk grant clamps the earned limit
	x := Ramp{Limit: 3, Good: RampSamples - 1}.Step(good, Pressure{}, 3, 3)
	if x.Limit != 3 || x.Good != 0 {
		t.Errorf("cap at maxN: %+v", x)
	}
	if x = (Ramp{Limit: 5, Good: 4}).Step(good, Pressure{}, 5, 2); x.Limit != 2 {
		t.Errorf("shrunk grant must clamp: %+v", x)
	}
	// no capacity: hold, no progress
	if x = (Ramp{Limit: 2, Good: 4}).Step(good, Pressure{}, 2, 0); x.Limit != 2 || x.Good != 0 {
		t.Errorf("maxN 0: %+v", x)
	}
}

func TestSampleFromCapacity(t *testing.T) {
	s := SampleFromCapacity(2000, 500, 4000, 1000)
	if s.CPUPercent != 25 || s.MemAvailPercent != 75 {
		t.Fatalf("got %+v", s)
	}
	if s := SampleFromCapacity(2000, 500, 4000, 9000); s.MemAvailPercent != 0 {
		t.Fatalf("over-used memory clamps to 0 avail: %+v", s)
	}
	var p Pressure
	for _, bad := range []PressureSample{
		SampleFromCapacity(0, 0, 4000, 1000),
		SampleFromCapacity(2000, -1, 4000, 1000),
		SampleFromCapacity(2000, 0, 0, 0),
		SampleFromCapacity(2000, 0, 4000, -1),
	} {
		if q := p.Step(bad, t0); !q.Reduced {
			t.Fatalf("unusable report must enter pressure: %+v", bad)
		}
	}
	w := WorstSample(PressureSample{CPUPercent: 20, MemAvailPercent: 50}, PressureSample{CPUPercent: 70, MemAvailPercent: 40})
	if w.CPUPercent != 70 || w.MemAvailPercent != 40 {
		t.Fatalf("worst: %+v", w)
	}
}
