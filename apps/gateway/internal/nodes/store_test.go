package nodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	pinA = strings.Repeat("a", 64)
	pinB = strings.Repeat("b", 64)
	pinC = strings.Repeat("c", 64)
)

func goodAlloc(id string) Allocation {
	return Allocation{
		NodeID: id, Region: "eu-west", Endpoint: "10.8.0.2:7443", URISAN: NodeURISAN(id),
		CPUMillis: 3000, MemoryBytes: 5 << 30, ReservationState: ReservationKnown, State: StateActive,
		MaxBrowserWorkloads: 4, ValidUntil: t0.Add(time.Hour), Generation: 5,
	}
}

func TestStoreAllocationValidate(t *testing.T) {
	if err := goodAlloc("helper-1").Validate(); err != nil {
		t.Fatalf("good allocation: %v", err)
	}
	voice := goodAlloc("helper-1")
	voice.VoiceCapable, voice.UDPPortMin, voice.UDPPortMax, voice.NATIP = true, 40000, 40100, "203.0.113.7"
	if err := voice.Validate(); err != nil {
		t.Fatalf("voice allocation: %v", err)
	}
	cases := map[string]func(*Allocation){
		"empty node id":        func(a *Allocation) { a.NodeID = "" },
		"leading dash id":      func(a *Allocation) { a.NodeID = "-x" },
		"long node id":         func(a *Allocation) { a.NodeID = strings.Repeat("n", 65) },
		"uppercase region":     func(a *Allocation) { a.Region = "EU" },
		"no port":              func(a *Allocation) { a.Endpoint = "10.8.0.2" },
		"no host":              func(a *Allocation) { a.Endpoint = ":7443" },
		"port zero":            func(a *Allocation) { a.Endpoint = "10.8.0.2:0" },
		"port too big":         func(a *Allocation) { a.Endpoint = "10.8.0.2:70000" },
		"uri san spoof":        func(a *Allocation) { a.URISAN = NodeURISAN("other") },
		"uri san empty":        func(a *Allocation) { a.URISAN = "" },
		"uppercase spki":       func(a *Allocation) { a.SPKISHA256 = strings.Repeat("A", 64) },
		"short spki":           func(a *Allocation) { a.SPKISHA256 = "abc" },
		"negative cpu":         func(a *Allocation) { a.CPUMillis = -1 },
		"huge cpu":             func(a *Allocation) { a.CPUMillis = 1_000_001 },
		"negative memory":      func(a *Allocation) { a.MemoryBytes = -1 },
		"bad reservation":      func(a *Allocation) { a.ReservationState = "maybe" },
		"empty reservation":    func(a *Allocation) { a.ReservationState = "" },
		"bad state":            func(a *Allocation) { a.State = "?" },
		"too many workloads":   func(a *Allocation) { a.MaxBrowserWorkloads = 65 },
		"negative workloads":   func(a *Allocation) { a.MaxBrowserWorkloads = -1 },
		"zero valid_until":     func(a *Allocation) { a.ValidUntil = time.Time{} },
		"negative generation":  func(a *Allocation) { a.Generation = -1 },
		"bad nat ip":           func(a *Allocation) { a.NATIP = "not-an-ip" },
		"reversed udp range":   func(a *Allocation) { a.UDPPortMin, a.UDPPortMax = 40100, 40000 },
		"low udp range":        func(a *Allocation) { a.UDPPortMin, a.UDPPortMax = 80, 90 },
		"voice without range":  func(a *Allocation) { a.VoiceCapable, a.NATIP = true, "203.0.113.7" },
		"voice without nat ip": func(a *Allocation) { a.VoiceCapable, a.UDPPortMin, a.UDPPortMax = true, 40000, 40100 },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			a := goodAlloc("helper-1")
			mut(&a)
			if err := a.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestStoreURISAN(t *testing.T) {
	if id, ok := NodeIDFromURISAN(NodeURISAN("helper-1")); !ok || id != "helper-1" {
		t.Fatalf("round trip = %q %v", id, ok)
	}
	for _, bad := range []string{"", "spiffe://ubag/node/", "spiffe://other/node/x", "spiffe://ubag/node/a/b", "https://ubag/node/x"} {
		if _, ok := NodeIDFromURISAN(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
}

func TestStoreRegistryMatches(t *testing.T) {
	e := RegistryEntry{NodeID: "helper-1", URISAN: NodeURISAN("helper-1"), SPKICurrent: pinA, SPKINext: pinB}
	for _, c := range []struct {
		name   string
		mut    func(*RegistryEntry)
		uri    string
		spki   string
		wantOK bool
	}{
		{"current pin", nil, e.URISAN, pinA, true},
		{"next pin", nil, e.URISAN, pinB, true},
		{"unknown pin", nil, e.URISAN, pinC, false},
		{"empty presented pin", nil, e.URISAN, "", false},
		{"uri san spoof", nil, NodeURISAN("other"), pinA, false},
		{"revoked", func(e *RegistryEntry) { e.RevokedAt = t0 }, e.URISAN, pinA, false},
		{"no pins never match", func(e *RegistryEntry) { e.SPKICurrent, e.SPKINext = "", "" }, e.URISAN, "", false},
		{"only next pinned", func(e *RegistryEntry) { e.SPKICurrent = "" }, e.URISAN, pinB, true},
		{"empty current is not a wildcard", func(e *RegistryEntry) { e.SPKICurrent = "" }, e.URISAN, pinC, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := e
			if c.mut != nil {
				c.mut(&e)
			}
			if got := e.Matches(c.uri, c.spki); got != c.wantOK {
				t.Fatalf("Matches = %v, want %v", got, c.wantOK)
			}
		})
	}
}

func TestStoreMemoryContract(t *testing.T) {
	testStoreContract(t, NewMemoryStore(), "mem")
}

// testStoreContract is the behaviour every Store must share. prefix keeps node
// ids unique so the Postgres run can share a database with other tests.
func testStoreContract(t *testing.T, s Store, prefix string) {
	ctx := context.Background()
	id := func(n string) string { return prefix + "-" + n }
	now := t0.Add(time.Minute)
	if err := s.Ready(ctx); err != nil {
		t.Fatalf("Ready: %v", err)
	}

	t.Run("allocation round trip and list order", func(t *testing.T) {
		b, a := goodAlloc(id("b")), goodAlloc(id("a"))
		a.VoiceCapable, a.UDPPortMin, a.UDPPortMax, a.NATIP = true, 40000, 40100, "203.0.113.7"
		a.SPKISHA256 = pinA
		for _, al := range []Allocation{b, a} {
			if err := s.ApplyAllocation(ctx, al, now); err != nil {
				t.Fatalf("apply %s: %v", al.NodeID, err)
			}
		}
		got, err := s.GetAllocation(ctx, a.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		a.AcceptedAt = now
		if !got.ValidUntil.Equal(a.ValidUntil) || !got.AcceptedAt.Equal(now) || !got.sameExceptValidity(a) {
			t.Fatalf("round trip lost data:\n got %+v\nwant %+v", got, a)
		}
		if _, err := s.GetAllocation(ctx, id("missing")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing allocation = %v", err)
		}
		var ours []string
		list, err := s.ListAllocations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, al := range list {
			if strings.HasPrefix(al.NodeID, prefix+"-") {
				ours = append(ours, al.NodeID)
			}
		}
		if len(ours) != 2 || ours[0] != id("a") || ours[1] != id("b") {
			t.Fatalf("list order = %v", ours)
		}
	})

	t.Run("invalid allocation is rejected", func(t *testing.T) {
		a := goodAlloc(id("inv"))
		a.State = "?"
		if err := s.ApplyAllocation(ctx, a, now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("apply invalid = %v", err)
		}
		if _, err := s.GetAllocation(ctx, a.NodeID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("invalid allocation was stored: %v", err)
		}
	})

	t.Run("generation fencing", func(t *testing.T) {
		g := goodAlloc(id("gen"))
		if err := s.ApplyAllocation(ctx, g, now); err != nil {
			t.Fatal(err)
		}
		lower := g
		lower.Generation = 4
		if err := s.ApplyAllocation(ctx, lower, now); !errors.Is(err, ErrStaleGeneration) {
			t.Fatalf("lower generation = %v", err)
		}
		if err := s.ApplyAllocation(ctx, g, now.Add(time.Second)); err != nil { // idempotent re-poll
			t.Fatalf("identical replay = %v", err)
		}
		renew := g
		renew.ValidUntil = g.ValidUntil.Add(time.Hour)
		if err := s.ApplyAllocation(ctx, renew, now.Add(2*time.Second)); err != nil {
			t.Fatalf("valid_until renewal = %v", err)
		}
		grown := g
		grown.MaxBrowserWorkloads = 8
		if err := s.ApplyAllocation(ctx, grown, now); !errors.Is(err, ErrGenerationConflict) {
			t.Fatalf("capacity change without bump = %v", err)
		}
		got, _ := s.GetAllocation(ctx, g.NodeID)
		if got.MaxBrowserWorkloads != 4 || !got.ValidUntil.Equal(renew.ValidUntil) || !got.AcceptedAt.Equal(now.Add(2*time.Second)) {
			t.Fatalf("rejected writes must not change the row: %+v", got)
		}
		grown.Generation = 6
		if err := s.ApplyAllocation(ctx, grown, now); err != nil {
			t.Fatalf("higher generation = %v", err)
		}
		if got, _ := s.GetAllocation(ctx, g.NodeID); got.Generation != 6 || got.MaxBrowserWorkloads != 8 {
			t.Fatalf("higher generation not applied: %+v", got)
		}
	})

	t.Run("state needs an allocation, never rewinds, round trips pressure", func(t *testing.T) {
		if err := s.PutState(ctx, HelperState{NodeID: id("nostate"), LastHeartbeat: now}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("state without allocation = %v", err)
		}
		if err := s.PutState(ctx, HelperState{NodeID: id("a")}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("state without heartbeat = %v", err)
		}
		if _, err := s.GetState(ctx, id("a")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("state before first heartbeat = %v", err)
		}
		reduced := Pressure{}.Step(PressureSample{CPUPercent: 90, MemAvailPercent: 50}, now)
		recovering := reduced.Step(PressureSample{CPUPercent: 10, MemAvailPercent: 90}, now)
		want := HelperState{NodeID: id("a"), LastHeartbeat: now, RampedLimit: 3, HostCores: 4, HostMemoryBytes: 8 << 30}.WithPressure(recovering)
		if !want.PressureReduced || want.PressureCalmSince.IsZero() {
			t.Fatalf("test setup: want a recovering state, got %+v", want)
		}
		if err := s.PutState(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetState(ctx, id("a"))
		if err != nil {
			t.Fatal(err)
		}
		if !got.LastHeartbeat.Equal(now) || !got.PressureCalmSince.Equal(now) || !got.PressureReduced ||
			got.RampedLimit != 3 || got.HostCores != 4 || got.HostMemoryBytes != 8<<30 {
			t.Fatalf("state round trip: %+v", got)
		}
		if p := got.Pressure(); p.Reduced != recovering.Reduced || !p.calmSince.Equal(recovering.calmSince) {
			t.Fatalf("pressure rebuilt as %+v, want %+v", p, recovering)
		}
		older := want
		older.LastHeartbeat, older.RampedLimit = now.Add(-time.Minute), 1
		if err := s.PutState(ctx, older); err != nil {
			t.Fatalf("stale state write must be ignored, not fail: %v", err)
		}
		if got, _ := s.GetState(ctx, id("a")); !got.LastHeartbeat.Equal(now) || got.RampedLimit != 3 {
			t.Fatalf("stale write rewound state: %+v", got)
		}
		newer := want.WithPressure(Pressure{})
		newer.LastHeartbeat = now.Add(time.Minute)
		if err := s.PutState(ctx, newer); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetState(ctx, id("a")); got.PressureReduced || !got.PressureCalmSince.IsZero() || !got.LastHeartbeat.Equal(newer.LastHeartbeat) {
			t.Fatalf("newer state not applied: %+v", got)
		}
	})

	t.Run("stored allocation and state feed placement", func(t *testing.T) {
		al, _ := s.GetAllocation(ctx, id("a"))
		st, _ := s.GetState(ctx, id("a"))
		at := st.LastHeartbeat.Add(time.Second)
		d := Evaluate(al.Grant(), st.LastHeartbeat, at, st.Pressure(), st.RampedLimit)
		if !d.Eligible || d.Limit != 3 {
			t.Fatalf("decision = %+v, want eligible limit 3", d)
		}
		if d := Evaluate(al.Grant(), st.LastHeartbeat, at.Add(time.Minute), st.Pressure(), st.RampedLimit); d.Eligible {
			t.Fatalf("missed heartbeats must stop placement: %+v", d)
		}
	})

	t.Run("registry pins, rotation and sticky revocation", func(t *testing.T) {
		n := id("reg")
		if _, err := s.GetRegistry(ctx, n); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing registry = %v", err)
		}
		if err := s.PutRegistry(ctx, RegistryEntry{NodeID: n, URISAN: NodeURISAN("other")}, now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("spoofed uri san = %v", err)
		}
		if err := s.PutRegistry(ctx, RegistryEntry{NodeID: n, URISAN: NodeURISAN(n), SPKICurrent: "xyz"}, now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad pin = %v", err)
		}
		if err := s.PromoteSPKI(ctx, n, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("promote unknown = %v", err)
		}
		if err := s.RevokeNode(ctx, n, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("revoke unknown = %v", err)
		}
		e := RegistryEntry{NodeID: n, URISAN: NodeURISAN(n), SPKICurrent: pinA}
		if err := s.PutRegistry(ctx, e, now); err != nil {
			t.Fatal(err)
		}
		if err := s.PromoteSPKI(ctx, n, now); !errors.Is(err, ErrInvalid) {
			t.Fatalf("promote without next = %v", err)
		}
		e.SPKINext = pinB
		if err := s.PutRegistry(ctx, e, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetRegistry(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		if got.SPKICurrent != pinA || got.SPKINext != pinB || got.Revoked() || !got.UpdatedAt.Equal(now.Add(time.Second)) {
			t.Fatalf("registry round trip: %+v", got)
		}
		if !got.Matches(e.URISAN, pinA) || !got.Matches(e.URISAN, pinB) || got.Matches(e.URISAN, pinC) {
			t.Fatal("both current and next pins must match during rotation, nothing else")
		}
		if err := s.PromoteSPKI(ctx, n, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		got, _ = s.GetRegistry(ctx, n)
		if got.SPKICurrent != pinB || got.SPKINext != "" || got.Matches(e.URISAN, pinA) {
			t.Fatalf("after promote: %+v", got)
		}
		if err := s.RevokeNode(ctx, n, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := s.RevokeNode(ctx, n, now.Add(time.Hour)); err != nil { // idempotent, first time wins
			t.Fatal(err)
		}
		got, _ = s.GetRegistry(ctx, n)
		if !got.Revoked() || !got.RevokedAt.Equal(now.Add(3*time.Second)) || got.Matches(e.URISAN, pinB) {
			t.Fatalf("after revoke: %+v", got)
		}
		if err := s.PutRegistry(ctx, e, now); !errors.Is(err, ErrNodeRevoked) {
			t.Fatalf("re-pin of a revoked node = %v", err)
		}
		if err := s.PromoteSPKI(ctx, n, now); !errors.Is(err, ErrNodeRevoked) {
			t.Fatalf("promote of a revoked node = %v", err)
		}
		if got, _ = s.GetRegistry(ctx, n); got.SPKICurrent != pinB || !got.Revoked() {
			t.Fatalf("revoked entry must not change: %+v", got)
		}
	})

	t.Run("revoked allocation fences the registry", func(t *testing.T) {
		pinned, bare := id("fence-pinned"), id("fence-bare")
		for _, n := range []string{pinned, bare} {
			if err := s.ApplyAllocation(ctx, goodAlloc(n), now); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.PutRegistry(ctx, RegistryEntry{NodeID: pinned, URISAN: NodeURISAN(pinned), SPKICurrent: pinA}, now); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{pinned, bare} {
			revoked := goodAlloc(n)
			revoked.State, revoked.Generation = StateRevoked, 6
			if err := s.ApplyAllocation(ctx, revoked, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		got, err := s.GetRegistry(ctx, pinned)
		if err != nil || !got.Revoked() || !got.RevokedAt.Equal(now.Add(time.Minute)) || got.Matches(NodeURISAN(pinned), pinA) {
			t.Fatalf("revoked allocation must revoke the pinned entry: %+v %v", got, err)
		}
		if err := s.PutRegistry(ctx, RegistryEntry{NodeID: bare, URISAN: NodeURISAN(bare), SPKICurrent: pinA}, now); !errors.Is(err, ErrNodeRevoked) {
			t.Fatalf("pinning a node whose allocation is revoked = %v", err)
		}
		if _, err := s.GetRegistry(ctx, bare); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a refused pin must not create an entry: %v", err)
		}
	})
}

func TestStoreMemoryNodeLimit(t *testing.T) {
	ctx, s := context.Background(), NewMemoryStore()
	for i := 0; i < MaxNodes; i++ {
		n := fmt.Sprintf("n%03d", i)
		if err := s.ApplyAllocation(ctx, goodAlloc(n), t0); err != nil {
			t.Fatalf("allocation %d: %v", i, err)
		}
		if err := s.PutRegistry(ctx, RegistryEntry{NodeID: n, URISAN: NodeURISAN(n), SPKICurrent: pinA}, t0); err != nil {
			t.Fatalf("registry %d: %v", i, err)
		}
	}
	if err := s.ApplyAllocation(ctx, goodAlloc("extra"), t0); !errors.Is(err, ErrTooManyNodes) {
		t.Fatalf("allocation past the cap = %v", err)
	}
	if err := s.PutRegistry(ctx, RegistryEntry{NodeID: "extra", URISAN: NodeURISAN("extra")}, t0); !errors.Is(err, ErrTooManyNodes) {
		t.Fatalf("registry past the cap = %v", err)
	}
	next := goodAlloc("n000")
	next.Generation++
	if err := s.ApplyAllocation(ctx, next, t0); err != nil {
		t.Fatalf("updating an existing node at the cap must work: %v", err)
	}
	if list, _ := s.ListAllocations(ctx); len(list) != MaxNodes {
		t.Fatalf("list size = %d", len(list))
	}
}

// Concurrent applies of every generation in scrambled order must end at the
// highest generation, and every loser must be a clean stale rejection.
func TestStoreMemoryConcurrentGenerations(t *testing.T) {
	ctx, s := context.Background(), NewMemoryStore()
	const top = 64
	var wg sync.WaitGroup
	errs := make(chan error, top)
	for g := 0; g <= top; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			a := goodAlloc("race")
			a.Generation = int64((g * 37) % (top + 1)) // permutation of 0..top
			a.MaxBrowserWorkloads = int(a.Generation % 8)
			if err := s.ApplyAllocation(ctx, a, t0); err != nil && !errors.Is(err, ErrStaleGeneration) && !errors.Is(err, ErrGenerationConflict) {
				errs <- err
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := s.GetAllocation(ctx, "race")
	if err != nil || got.Generation != top {
		t.Fatalf("final generation = %d (%v), want %d", got.Generation, err, top)
	}
}

func TestStorePostgresNilSafety(t *testing.T) {
	var s Store = NewPostgresStore(nil)
	ctx := context.Background()
	checks := map[string]error{
		"Ready":           s.Ready(ctx),
		"ApplyAllocation": s.ApplyAllocation(ctx, goodAlloc("helper-1"), t0),
		"PutState":        s.PutState(ctx, HelperState{NodeID: "helper-1", LastHeartbeat: t0}),
		"PutRegistry":     s.PutRegistry(ctx, RegistryEntry{NodeID: "helper-1", URISAN: NodeURISAN("helper-1")}, t0),
		"PromoteSPKI":     s.PromoteSPKI(ctx, "helper-1", t0),
		"RevokeNode":      s.RevokeNode(ctx, "helper-1", t0),
	}
	_, checks["GetAllocation"] = s.GetAllocation(ctx, "helper-1")
	_, checks["ListAllocations"] = s.ListAllocations(ctx)
	_, checks["GetState"] = s.GetState(ctx, "helper-1")
	_, checks["GetRegistry"] = s.GetRegistry(ctx, "helper-1")
	for name, err := range checks {
		if !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%s with no db = %v, want ErrNotConfigured", name, err)
		}
	}
}
