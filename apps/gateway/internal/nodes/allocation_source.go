package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Environment names for the allocation poller (UBAG_HELPER_NODES gates the
// whole helper plane; these only configure where grants come from).
const (
	EnvFleetManagerURL    = "UBAG_FLEET_MANAGER_URL"
	EnvFleetManagerToken  = "UBAG_FLEET_MANAGER_TOKEN"
	EnvFleetPollSeconds   = "UBAG_FLEET_POLL_SECONDS"
	EnvFleetStaleGraceSec = "UBAG_FLEET_GRANT_STALE_GRACE_SECONDS"

	DefaultPollInterval = 30 * time.Second
	DefaultStaleGrace   = 10 * time.Minute

	minPollInterval = 5 * time.Second
	maxPollInterval = time.Hour
	maxStaleGrace   = 24 * time.Hour

	// maxResponseBytes bounds the polled body: 256 allocations are well under
	// 300 KiB, so 1 MiB is generous and still a hard ceiling.
	maxResponseBytes = 1 << 20
)

// ErrBadResponse is returned for any manager response UBAG cannot trust
// (transport success but unparseable, oversized, schema-violating, unexpected
// status). The whole response is discarded; the last-known-good grants stay.
var ErrBadResponse = errors.New("nodes: untrusted manager response")

// Snapshot is one fetch of the manager's allocation_list.
type Snapshot struct {
	NotModified bool // 304: nothing changed since the ETag sent
	ETag        string
	Allocations []Allocation
}

// AllocationSource is where grants come from. The only production
// implementation is HTTPSource; the Fleet Manager side is external and not in
// this repo (ADR-0006), so tests use a fake HTTP server.
type AllocationSource interface {
	// Fetch returns the full list, or NotModified when etag still matches.
	// Any parse or schema failure is an error wrapping ErrBadResponse.
	Fetch(ctx context.Context, etag string) (Snapshot, error)
}

// HTTPSource polls a manager endpoint that serves the allocation_list
// definition of node-allocation.schema.json with ETag support.
type HTTPSource struct {
	url   string
	token string
	client *http.Client
}

var _ AllocationSource = (*HTTPSource)(nil)

// maxTokenBytes bounds the bearer token: even a generous manager token is a
// few hundred bytes, so 4 KiB rejects accidental pastes of a whole file.
const maxTokenBytes = 4096

// NewHTTPSource validates rawURL (http or https, host required, no embedded
// credentials) and builds a source. A non-empty token is sent as the
// Authorization: Bearer credential on every poll (the manager side defines
// what it accepts; an empty token sends no Authorization header at all).
// Redirects are never followed: a manager that redirects is treated as a bad
// response, not chased to another host. A nil client uses a 10 s timeout
// default.
func NewHTTPSource(rawURL, token string, client *http.Client) (*HTTPSource, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%w: manager url must be http(s) with a host and no credentials", ErrInvalid)
	}
	token = strings.TrimSpace(token)
	if token != "" {
		// Reject anything http.Header would refuse at write time anyway, or that
		// could smuggle a second header: printable ASCII without spaces only.
		for _, r := range token {
			if r <= 0x20 || r >= 0x7f {
				return nil, fmt.Errorf("%w: %s must be a single header-safe value", ErrInvalid, EnvFleetManagerToken)
			}
		}
		if len(token) > maxTokenBytes {
			return nil, fmt.Errorf("%w: %s over %d bytes", ErrInvalid, EnvFleetManagerToken, maxTokenBytes)
		}
	}
	c := http.Client{Timeout: 10 * time.Second}
	if client != nil {
		c = *client
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &HTTPSource{url: u.String(), token: token, client: &c}, nil
}

func (s *HTTPSource) Fetch(ctx context.Context, etag string) (Snapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Accept", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Snapshot{}, fmt.Errorf("nodes: manager unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return Snapshot{NotModified: true, ETag: etag}, nil
	case resp.StatusCode != http.StatusOK:
		return Snapshot{}, fmt.Errorf("%w: status %d", ErrBadResponse, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("nodes: manager read failed: %w", err)
	}
	if len(body) > maxResponseBytes {
		return Snapshot{}, fmt.Errorf("%w: body over %d bytes", ErrBadResponse, maxResponseBytes)
	}
	list, err := parseAllocationList(body)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{ETag: resp.Header.Get("ETag"), Allocations: list}, nil
}

// wire shapes: strict (unknown fields rejected), mirroring the schema's
// additionalProperties:false.
type wireList struct {
	SchemaVersion int         `json:"schema_version"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Allocations   []wireAlloc `json:"allocations"`
}

type wireAlloc struct {
	SchemaVersion int    `json:"schema_version"`
	NodeID        string `json:"node_id"`
	Region        string `json:"region"`
	Endpoint      string `json:"endpoint"`
	CertIdentity  struct {
		URISAN     string `json:"uri_san"`
		SPKISHA256 string `json:"spki_sha256"`
	} `json:"cert_identity"`
	CPUMillis           int    `json:"cpu_millis"`
	MemoryBytes         int64  `json:"memory_bytes"`
	ReservationState    string `json:"reservation_state"`
	State               string `json:"state"`
	MaxBrowserWorkloads int    `json:"max_browser_workloads"`
	VoiceCapable        bool   `json:"voice_capable"`
	UDPPortRange        *struct {
		Min int `json:"min"`
		Max int `json:"max"`
	} `json:"udp_port_range"`
	NATIP      string    `json:"nat_ip"`
	ValidUntil time.Time `json:"valid_until"`
	Generation int64     `json:"generation"`
}

// parseAllocationList is all-or-nothing: one bad entry rejects the list, so a
// half-understood manager response never partially changes grants (fail
// closed). Required-field presence is enforced by Allocation.Validate (zero
// values for required strings and valid_until fail it).
func parseAllocationList(body []byte) ([]Allocation, error) {
	bad := func(format string, args ...any) ([]Allocation, error) {
		return nil, fmt.Errorf("%w: "+format, append([]any{ErrBadResponse}, args...)...)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var l wireList
	if err := dec.Decode(&l); err != nil {
		return bad("json")
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad("trailing data")
	}
	if l.SchemaVersion != 1 || l.GeneratedAt.IsZero() || l.Allocations == nil {
		return bad("list header")
	}
	if len(l.Allocations) > MaxNodes {
		return bad("more than %d allocations", MaxNodes)
	}
	out := make([]Allocation, 0, len(l.Allocations))
	seen := make(map[string]bool, len(l.Allocations))
	for i, w := range l.Allocations {
		a := Allocation{
			NodeID: w.NodeID, Region: w.Region, Endpoint: w.Endpoint, URISAN: w.CertIdentity.URISAN,
			SPKISHA256: w.CertIdentity.SPKISHA256, CPUMillis: w.CPUMillis, MemoryBytes: w.MemoryBytes,
			ReservationState: w.ReservationState, State: w.State, MaxBrowserWorkloads: w.MaxBrowserWorkloads,
			VoiceCapable: w.VoiceCapable, NATIP: w.NATIP, ValidUntil: w.ValidUntil, Generation: w.Generation,
		}
		if w.UDPPortRange != nil {
			a.UDPPortMin, a.UDPPortMax = w.UDPPortRange.Min, w.UDPPortRange.Max
		}
		if w.SchemaVersion != 1 || a.Validate() != nil || seen[a.NodeID] {
			return bad("allocation %d", i)
		}
		seen[a.NodeID] = true
		out = append(out, a)
	}
	return out, nil
}

// PollerConfig configures a Poller.
type PollerConfig struct {
	Interval   time.Duration // time between polls
	StaleGrace time.Duration // how long last-known-good stays usable with the manager unreachable
}

// PollerConfigFromEnv reads the UBAG_FLEET_* variables via lookup (e.g.
// os.LookupEnv). An unset or empty URL means "not configured" (ok=false, no
// error). The token is returned verbatim for NewHTTPSource (empty = poll
// unauthenticated, exactly the pre-token behaviour). Any malformed or
// out-of-range value is an error: fail closed at startup rather than silently
// running with a surprising grace.
func PollerConfigFromEnv(lookup func(string) (string, bool)) (rawURL, token string, cfg PollerConfig, ok bool, err error) {
	get := func(k string) string { v, _ := lookup(k); return strings.TrimSpace(v) }
	if rawURL = get(EnvFleetManagerURL); rawURL == "" {
		return "", "", PollerConfig{}, false, nil
	}
	token = get(EnvFleetManagerToken)
	cfg = PollerConfig{Interval: DefaultPollInterval, StaleGrace: DefaultStaleGrace}
	secs := func(k string, lo, hi time.Duration, dst *time.Duration) error {
		v := get(k)
		if v == "" {
			return nil
		}
		n, e := strconv.Atoi(v)
		d := time.Duration(n) * time.Second
		if e != nil || d < lo || d > hi {
			return fmt.Errorf("%s must be %d..%d seconds", k, int(lo/time.Second), int(hi/time.Second))
		}
		*dst = d
		return nil
	}
	if err = secs(EnvFleetPollSeconds, minPollInterval, maxPollInterval, &cfg.Interval); err != nil {
		return "", "", PollerConfig{}, false, err
	}
	if err = secs(EnvFleetStaleGraceSec, 0, maxStaleGrace, &cfg.StaleGrace); err != nil {
		return "", "", PollerConfig{}, false, err
	}
	return rawURL, token, cfg, true, nil
}

// Poller copies the manager's grants into the Store and degrades them when the
// manager goes quiet (D3):
//   - Only a successfully fetched, fully valid list changes grants, so while
//     the manager is down nothing is ever granted or increased; the last
//     accepted grant keeps governing (valid_until still expires it).
//   - After StaleGrace without a good poll, Current reports every active grant
//     as draining: no new placements. Work already running is never touched
//     here.
//   - A node missing from a good list is reported draining (not revoked: the
//     manager never said so).
type Poller struct {
	src   AllocationSource
	store Store
	cfg   PollerConfig
	now   func() time.Time

	mu          sync.Mutex
	etag        string
	lastSuccess time.Time       // zero until this process gets a good poll
	absent      map[string]bool // node ids the last good list did not include
}

// NewPoller wires a source to a store. A nil now uses time.Now.
func NewPoller(src AllocationSource, store Store, cfg PollerConfig, now func() time.Time) *Poller {
	if now == nil {
		now = time.Now
	}
	return &Poller{src: src, store: store, cfg: cfg, now: now, absent: map[string]bool{}}
}

// Poll performs one fetch-and-apply. A nil error means the manager answered
// with something trustworthy (including 304); entries the store fenced off as
// stale are skipped and logged, never fatal. Any other error leaves grants and
// the ETag untouched and does not refresh the freshness clock.
func (p *Poller) Poll(ctx context.Context) error {
	p.mu.Lock()
	etag := p.etag
	p.mu.Unlock()

	snap, err := p.src.Fetch(ctx, etag)
	if err != nil {
		return err
	}
	now := p.now()
	if snap.NotModified {
		p.succeed(now, etag, nil)
		return nil
	}
	listed := make(map[string]bool, len(snap.Allocations))
	for _, a := range snap.Allocations {
		listed[a.NodeID] = true
		switch err := p.store.ApplyAllocation(ctx, a, now); {
		case err == nil:
		case errors.Is(err, ErrStaleGeneration), errors.Is(err, ErrGenerationConflict), errors.Is(err, ErrTooManyNodes):
			slog.Warn("fleet allocation rejected, keeping last-known-good", "node_id", a.NodeID, "reason", err.Error())
		default:
			return err // store trouble: retry the whole list next tick
		}
	}
	existing, err := p.store.ListAllocations(ctx)
	if err != nil {
		return err
	}
	absent := map[string]bool{}
	for _, e := range existing {
		if !listed[e.NodeID] {
			absent[e.NodeID] = true
		}
	}
	p.succeed(now, snap.ETag, absent)
	return nil
}

// succeed records a good poll; absent == nil (304) keeps the previous set.
func (p *Poller) succeed(now time.Time, etag string, absent map[string]bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastSuccess, p.etag = now, etag
	if absent != nil {
		p.absent = absent
	}
}

// Run polls immediately, then every Interval until ctx ends. Errors are logged
// (without response bodies); the loop never exits on them.
func (p *Poller) Run(ctx context.Context) {
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		if err := p.Poll(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("fleet allocation poll failed", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// stale reports whether a grant has outlived the grace: freshness is the last
// good poll of this process, or, before one, the time the store accepted the
// grant (so a restart with the manager down keeps the persisted grant for the
// remaining grace instead of dropping it at once). Caller holds p.mu.
func (p *Poller) stale(a Allocation, now time.Time) bool {
	ref := a.AcceptedAt
	if !p.lastSuccess.IsZero() {
		ref = p.lastSuccess
	}
	return now.Sub(ref) > p.cfg.StaleGrace
}

// Current returns the stored allocations as placement may use them at now:
// active grants become draining when stale or absent from the manager's latest
// list. draining and revoked pass through. Never increases anything.
func (p *Poller) Current(ctx context.Context, now time.Time) ([]Allocation, error) {
	all, err := p.store.ListAllocations(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range all {
		if a.State == StateActive && (p.absent[a.NodeID] || p.stale(a, now)) {
			all[i].State = StateDraining
		}
	}
	return all, nil
}
