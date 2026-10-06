package nodes

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func okGrant() Grant {
	return Grant{State: "active", ReservationState: "known", MaxBrowserWorkloads: 4, ValidUntil: t0.Add(time.Hour)}
}

func TestLogicEligibility(t *testing.T) {
	cases := []struct {
		name   string
		mut    func(*Grant)
		since  time.Duration // since last heartbeat
		wantOK bool
		reason string
	}{
		{"healthy", nil, 0, true, ""},
		{"two missed", nil, 44 * time.Second, true, ""},
		{"three missed stops", nil, 45 * time.Second, false, ReasonHeartbeatMissed},
		{"reservation unknown", func(g *Grant) { g.ReservationState = "unknown" }, 0, false, ReasonReservationUnk},
		{"reservation empty", func(g *Grant) { g.ReservationState = "" }, 0, false, ReasonReservationUnk},
		{"draining", func(g *Grant) { g.State = "draining" }, 0, false, ReasonDraining},
		{"revoked", func(g *Grant) { g.State = "revoked" }, 0, false, ReasonRevoked},
		{"unknown state fails closed", func(g *Grant) { g.State = "?" }, 0, false, ReasonDraining},
		{"expired", func(g *Grant) { g.ValidUntil = t0.Add(time.Second) }, 2 * time.Second, false, ReasonGrantExpired},
		{"no valid_until", func(g *Grant) { g.ValidUntil = time.Time{} }, 0, false, ReasonGrantExpired},
		{"zero workloads", func(g *Grant) { g.MaxBrowserWorkloads = 0 }, 0, false, ReasonNoCapacity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := okGrant()
			if c.mut != nil {
				c.mut(&g)
			}
			d := Evaluate(g, t0, t0.Add(c.since), Pressure{}, 0)
			if d.Eligible != c.wantOK || d.Reason != c.reason {
				t.Fatalf("got %+v want ok=%v reason=%q", d, c.wantOK, c.reason)
			}
			if !d.Eligible && d.Limit != 0 {
				t.Fatalf("ineligible limit = %d", d.Limit)
			}
		})
	}
	if d := Evaluate(okGrant(), time.Time{}, t0, Pressure{}, 0); d.Eligible {
		t.Fatal("never-seen heartbeat must be ineligible")
	}
}

func TestLogicStartAtOneThenRamp(t *testing.T) {
	g := okGrant()
	for _, c := range []struct {
		name   string
		p      Pressure
		ramped int
		want   int
	}{
		{"new helper starts at 1", Pressure{}, 0, 1},
		{"ramped", Pressure{}, 3, 3},
		{"clamps to grant max", Pressure{}, 99, 4},
		{"reduced halves", Pressure{Reduced: true}, 4, 2},
		{"reduced never below 1", Pressure{Reduced: true}, 0, 1},
	} {
		if d := Evaluate(g, t0, t0, c.p, c.ramped); d.Limit != c.want {
			t.Errorf("%s: limit = %d, want %d", c.name, d.Limit, c.want)
		}
	}
}

func TestLogicPressureHysteresis(t *testing.T) {
	var p Pressure
	now := t0
	step := func(cpu, mem float64, adv time.Duration) {
		now = now.Add(adv)
		p = p.Step(PressureSample{CPUPercent: cpu, MemAvailPercent: mem}, now)
	}
	step(80, 30, 0) // exactly 80 is not above
	if p.Reduced {
		t.Fatal("cpu==80 must not reduce")
	}
	step(81, 30, time.Second)
	if !p.Reduced {
		t.Fatal("cpu>80 must reduce")
	}
	step(50, 40, time.Second)
	step(50, 40, 118*time.Second)
	step(60, 40, time.Second) // cpu==60 is not under 60: dead band resets the window
	step(50, 40, time.Second)
	step(50, 40, 119*time.Second)
	if !p.Reduced {
		t.Fatal("window must restart after dead-band sample; 119s is not enough")
	}
	step(50, 40, time.Second) // 120s calm
	if p.Reduced {
		t.Fatal("recover after 2 min under 60% cpu and over 25% mem")
	}
	step(10, 19, time.Second)
	if !p.Reduced {
		t.Fatal("mem<20 must reduce")
	}
	step(10, 25, time.Second)
	step(10, 25, 5*time.Minute)
	if !p.Reduced {
		t.Fatal("mem==25 is not above 25")
	}
	step(10, 26, time.Second)
	step(95, 26, time.Minute) // hot sample cancels the open window
	step(10, 26, time.Second)
	step(10, 26, 119*time.Second)
	if !p.Reduced {
		t.Fatal("hot sample must cancel the recovery window")
	}
	step(10, 26, time.Second)
	if p.Reduced {
		t.Fatal("should recover")
	}
}

func TestLogicCeiling(t *testing.T) {
	const gb = int64(1) << 30
	cases := []struct {
		cores int
		mem   int64
		cpu   int
		memB  int64
	}{
		{2, 4 * gb, 1500, 5 * gb / 2},
		{4, 8 * gb, 3000, 5 * gb},
		{8, 16 * gb, 6000, 10 * gb},
		{0, 8 * gb, 0, 0},
		{4, 0, 0, 0},
	}
	for _, c := range cases {
		got := CeilingFor(c.cores, c.mem)
		if got.CPUMillis != c.cpu || got.MemoryBytes != c.memB {
			t.Errorf("CeilingFor(%d,%d) = %+v want %d/%d", c.cores, c.mem, got, c.cpu, c.memB)
		}
	}
}

func TestLogicEffectiveCapIsMinOfGrantAndCeiling(t *testing.T) {
	const gb = int64(1) << 30
	c := CeilingFor(2, 4*gb) // 1500 / 2.5 GiB
	for _, tc := range []struct {
		name    string
		cpu     int
		mem     int64
		wantCPU int
		wantMem int64
	}{
		{"grant above ceiling clamps", 4000, 8 * gb, 1500, 5 * gb / 2},
		{"grant below ceiling kept", 1000, gb, 1000, gb},
		{"negative clamps to 0", -5, -5, 0, 0},
	} {
		cpu, mem := EffectiveCap(tc.cpu, tc.mem, c)
		if cpu != tc.wantCPU || mem != tc.wantMem {
			t.Errorf("%s: got %d/%d want %d/%d", tc.name, cpu, mem, tc.wantCPU, tc.wantMem)
		}
	}
}
