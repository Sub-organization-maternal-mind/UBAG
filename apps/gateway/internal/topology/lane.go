package topology

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Browser lanes: voice excludes text and file jobs on one physical browser.
//
// A live voice session owns its browser environment exclusively: the virtual
// microphone and speaker monitor are instance-wide devices, and the provider
// tab is driven by the voice control jobs. Ordinary jobs on the same browser
// would change tabs, provider state or the audio path under a live call, and a
// voice session started under a running job would do the same to it. So the two
// are mutually exclusive on the lane, the browser's identity (BrowserLaneKey).
//
// Jobs on one lane do NOT exclude each other here; their ordering is the worker
// pool's business (one active job per physical session, ADR-0011).
//
// The registry keeps only the TRANSIENT halves of that exclusion:
//
//   - a job registers on the lane (LaneJob) for as long as it runs;
//   - a voice admission registers (LaneVoice) only for the duration of its
//     Reserve/Claim call.
//
// A live session's long-lived hold is the voice store's own lease (including its
// terminating hold), which is the truth for every replica and every restart; the
// consumer reads it through a probe. Both sides follow the same order, which is
// what makes the exclusion race-free without a lock across the two stores:
//
//	job:   register LaneJob, then look for a voice admission or a live session
//	voice: register LaneVoice, then look for a running job, then Reserve/Claim,
//	       and only then release LaneVoice
//
// If a job slipped past the voice side's look, it registered after that look,
// so its own look comes after the voice registration and sees either the
// admission still in flight or, once that is released, the committed lease.
//
// Registrations are process-local counts by default. UseLaneBackend moves them to
// the shared token store (expiring tokens, renewed by their holder) for a
// multi-replica deployment whose voice store is shared.

// LaneKind says which side of the exclusion a registration is.
type LaneKind string

const (
	// LaneJob is a running browser-driving job.
	LaneJob LaneKind = "job"
	// LaneVoice is a voice admission in flight.
	LaneVoice LaneKind = "voice"
)

// laneTokenTTL bounds a shared registration whose holder died: a crashed gateway
// frees the lane after at most this long. Holders renew well inside it (the
// consumer's heartbeat is 10 s; a voice admission lasts milliseconds).
const laneTokenTTL = 90 * time.Second

// BrowserLaneKey names the physical browser behind a CDP endpoint, or "" when
// there is no endpoint (a local, per-job browser has no shared lane). The key is
// tenant-free: two tenants on one browser are one lane. It identifies the browser
// by host and port, so "http://browser:9222" and "ws://browser:9222/devtools/..."
// are the same lane (the worker reports one spelling, the gateway env another).
func BrowserLaneKey(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	where := strings.ToLower(strings.TrimRight(endpoint, "/"))
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		where = strings.ToLower(u.Host)
	}
	return "browser:" + where
}

func laneTokenKey(kind LaneKind, lane string) string {
	return "lane-" + string(kind) + ":" + lane
}

// UseLaneBackend makes browser-lane registrations shared: tokens in the shared
// admission store instead of process-local counts. Call it once, before the
// registry serves traffic.
func (r *ConcurrencyRegistry) UseLaneBackend(backend TokenBackend) {
	if r != nil {
		r.laneBackend = backend
	}
}

// LaneHold is one registration on a browser lane. Release it exactly when the
// work it stands for ends; both methods are nil-safe and Release is idempotent.
type LaneHold struct {
	r     *ConcurrencyRegistry
	kind  LaneKind
	lane  string
	token string // shared token id; "" for a process-local count
	once  sync.Once
}

// EnterLane registers kind on lane. It never refuses: whether the other side is
// there is the caller's next question (LaneHolders). An error means the shared
// store could not record the registration, and the caller must fail closed. An
// empty lane (no shared browser) or a nil registry registers nothing and returns
// a nil hold.
func (r *ConcurrencyRegistry) EnterLane(ctx context.Context, kind LaneKind, lane string) (*LaneHold, error) {
	if r == nil || lane == "" {
		return nil, nil
	}
	if r.laneBackend != nil {
		token, _, err := r.laneBackend.AcquireToken(ctx,
			[]Lane{{Key: laneTokenKey(kind, lane)}}, laneTokenTTL, time.Now().UTC()) // no Cap: unlimited
		if err != nil {
			return nil, err
		}
		return &LaneHold{r: r, kind: kind, lane: lane, token: token}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lanes == nil {
		r.lanes = map[string]map[LaneKind]int{}
	}
	if r.lanes[lane] == nil {
		r.lanes[lane] = map[LaneKind]int{}
	}
	r.lanes[lane][kind]++
	return &LaneHold{r: r, kind: kind, lane: lane}, nil
}

// LaneHolders counts the live registrations of kind on lane.
func (r *ConcurrencyRegistry) LaneHolders(ctx context.Context, kind LaneKind, lane string) (int, error) {
	if r == nil || lane == "" {
		return 0, nil
	}
	if r.laneBackend != nil {
		return r.laneBackend.LaneLive(ctx, laneTokenKey(kind, lane), time.Now().UTC())
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lanes[lane][kind], nil
}

// Renew extends a shared registration (a process-local one never lapses). It
// returns ErrTokenLost once the registration is gone, which the holder must treat
// as losing the lane.
func (h *LaneHold) Renew(ctx context.Context) error {
	if h == nil || h.token == "" {
		return nil
	}
	return h.r.laneBackend.RenewToken(ctx, h.token, laneTokenTTL, time.Now().UTC())
}

// Release ends the registration.
func (h *LaneHold) Release() {
	if h == nil {
		return
	}
	h.once.Do(func() {
		if h.token != "" {
			ctx, cancel := context.WithTimeout(context.Background(), backendTimeout)
			defer cancel()
			if err := h.r.laneBackend.ReleaseToken(ctx, h.token); err != nil {
				slog.Warn("browser lane: releasing a registration failed (it will expire)", "kind", h.kind, "error", err)
			}
			return
		}
		h.r.mu.Lock()
		defer h.r.mu.Unlock()
		if n := h.r.lanes[h.lane][h.kind]; n > 1 {
			h.r.lanes[h.lane][h.kind] = n - 1
		} else if byKind := h.r.lanes[h.lane]; byKind != nil {
			delete(byKind, h.kind)
			if len(byKind) == 0 {
				delete(h.r.lanes, h.lane)
			}
		}
	})
}
