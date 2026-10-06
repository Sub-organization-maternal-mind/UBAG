package nodes

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Report is one helper's ReportCapacity answer, reduced to what placement reads.
type Report struct {
	CPUMillisTotal, CPUMillisUsed     int32
	MemoryBytesTotal, MemoryBytesUsed int64
	BrowsersMax, BrowsersActive       int32
	WorkloadVersion, RegistryDigest   string
}

// Reporter asks one helper for its capacity report (the primary dials the helper
// over mTLS, decision D3, so the implementation lives where the dialer does).
type Reporter interface {
	Report(ctx context.Context, a Allocation) (Report, error)
}

// Probe results, for the OnProbe hook (a bounded set: they become metric labels).
const (
	ProbeOK           = "ok"
	ProbeError        = "error"
	ProbeIncompatible = "incompatible"
)

const (
	defaultProbeTimeout  = 5 * time.Second
	defaultProbeParallel = 8
)

// ProberConfig configures a Prober.
type ProberConfig struct {
	Store    Store
	Reporter Reporter
	// Interval defaults to HeartbeatInterval (15 s); Timeout bounds one report
	// (default 5 s); Parallel bounds the reports in flight (default 8).
	Interval time.Duration
	Timeout  time.Duration
	Parallel int
	// WorkloadVersion and RegistryDigest, when set, are what this primary
	// dispatches to. A helper that reports anything else is not given a
	// heartbeat, so it takes no placements: the remote runner would refuse it at
	// the handshake and the placer would keep choosing it.
	WorkloadVersion string
	RegistryDigest  string
	// Now is the clock; nil means time.Now. The heartbeat is stamped with the
	// primary's clock, never the helper's, so helper clock skew cannot make a
	// silent node look alive.
	Now func() time.Time
	// OnProbe observes every report (metrics). Must not block.
	OnProbe func(result string)
}

// Prober is the heartbeat source of the helper plane (ADR-0009). Every interval it
// asks each helper that is not revoked for its capacity report and writes the
// node's runtime state: heartbeat time, pressure hysteresis from the helper's own
// cgroup numbers, the earned 1-to-N ramp and the host size the ceiling table
// needs. It never measures a helper itself. A report that fails, times out or
// names another workload leaves the state untouched, so the heartbeat ages and
// placement stops after three missed beats (45 s): silence fails closed.
type Prober struct {
	cfg ProberConfig

	mu   sync.Mutex
	good map[string]int  // ramp run per node (not persisted: a restart restarts the run)
	up   map[string]bool // last result per node, to log changes once
}

// NewProber applies defaults to cfg.
func NewProber(cfg ProberConfig) *Prober {
	cfg.Interval = durOr(cfg.Interval, HeartbeatInterval)
	cfg.Timeout = durOr(cfg.Timeout, defaultProbeTimeout)
	if cfg.Parallel <= 0 {
		cfg.Parallel = defaultProbeParallel
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Prober{cfg: cfg, good: map[string]int{}, up: map[string]bool{}}
}

func durOr(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Run probes immediately, then every Interval until ctx ends.
func (p *Prober) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		if err := p.Tick(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("helper probe round failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one round: every allocation that is not revoked is probed, at most
// Parallel at a time, and the round returns when all have answered or timed out.
func (p *Prober) Tick(ctx context.Context) error {
	allocs, err := p.cfg.Store.ListAllocations(ctx)
	if err != nil {
		return err
	}
	sem := make(chan struct{}, p.cfg.Parallel)
	var wg sync.WaitGroup
	for _, a := range allocs {
		if a.State == StateRevoked {
			continue // the dial would be refused: the node is no longer trusted
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			p.probe(ctx, a)
		}()
	}
	wg.Wait()
	return nil
}

func (p *Prober) probe(ctx context.Context, a Allocation) {
	rctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	rep, err := p.cfg.Reporter.Report(rctx, a)
	if err != nil {
		p.note(a.NodeID, ProbeError, err.Error())
		return
	}
	if (p.cfg.WorkloadVersion != "" && rep.WorkloadVersion != p.cfg.WorkloadVersion) ||
		(p.cfg.RegistryDigest != "" && rep.RegistryDigest != p.cfg.RegistryDigest) {
		p.note(a.NodeID, ProbeIncompatible, "workload version or adapter registry digest differs from this primary's")
		return
	}
	now := p.cfg.Now()
	st, err := p.cfg.Store.GetState(ctx, a.NodeID)
	if errors.Is(err, ErrNotFound) {
		st, err = HelperState{NodeID: a.NodeID}, nil
	}
	if err != nil {
		p.note(a.NodeID, ProbeError, err.Error())
		return
	}

	sample := SampleFromCapacity(rep.CPUMillisTotal, rep.CPUMillisUsed, rep.MemoryBytesTotal, rep.MemoryBytesUsed)
	pressure := st.Pressure().Step(sample, now)
	// The report's totals are the helper's own budget (its cgroup, or the machine
	// when none is set): that is the host size the ceiling table is applied to. A
	// zero or unusable total leaves the size unknown and the ceiling at 0 (fail closed).
	st.HostCores, st.HostMemoryBytes = 0, 0
	if rep.CPUMillisTotal > 0 && rep.MemoryBytesTotal > 0 {
		st.HostCores, st.HostMemoryBytes = int((int64(rep.CPUMillisTotal)+999)/1000), rep.MemoryBytesTotal
	}
	maxN := clampToCeiling(min(a.MaxBrowserWorkloads, int(max(rep.BrowsersMax, 0))), a, st)

	p.mu.Lock()
	ramp := Ramp{Limit: st.RampedLimit, Good: p.good[a.NodeID]}.Step(sample, pressure, int(max(rep.BrowsersActive, 0)), maxN)
	p.good[a.NodeID] = ramp.Good
	p.mu.Unlock()

	st = st.WithPressure(pressure)
	st.RampedLimit, st.LastHeartbeat = ramp.Limit, now
	if err := p.cfg.Store.PutState(ctx, st); err != nil {
		p.note(a.NodeID, ProbeError, err.Error())
		return
	}
	p.note(a.NodeID, ProbeOK, "")
}

// note counts a result and logs only when a node's result changes.
func (p *Prober) note(nodeID, result, detail string) {
	if p.cfg.OnProbe != nil {
		p.cfg.OnProbe(result)
	}
	ok := result == ProbeOK
	p.mu.Lock()
	prev, seen := p.up[nodeID]
	p.up[nodeID] = ok
	p.mu.Unlock()
	switch {
	case !ok && (!seen || prev):
		slog.Warn("helper capacity report failed; the node takes no new placements once its heartbeat is stale",
			"node_id", nodeID, "result", result, "detail", detail)
	case ok && seen && !prev:
		slog.Info("helper capacity reports are back", "node_id", nodeID)
	}
}
