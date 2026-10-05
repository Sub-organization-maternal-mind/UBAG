package httpapi

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Overload protection: the gateway bounds what it will hold in memory and how
// many requests it will serve at once, and answers anything beyond that with
// an explicit, retryable 429/503 (Retry-After is added centrally by
// writeError) rather than queueing without limit or running out of memory.
const (
	defaultMaxInflightRequests = 2000
	// defaultUploadMemoryBytes bounds the request-body bytes held in memory at
	// once across ALL uploads (facade multimodal bodies, artifact PUTs,
	// transcription audio). The gateway container is sized in the low GiB, so
	// 100 concurrent large bodies must not be admitted just because each one
	// is individually under its per-request limit.
	defaultUploadMemoryBytes int64 = 256 << 20
	// facadeDecodeFactor accounts for the raw body, the decoded JSON tree and
	// the decoded base64 payloads that coexist while a facade request is
	// parsed.
	facadeDecodeFactor = 3
)

// overloadReasons is the closed set of rejection reasons (low cardinality).
var overloadReasons = []string{"inflight_requests", "upload_memory", "concurrency", "queue_depth", "rate_limit", "voice_queue", "admission_error"}

// byteBudget is a non-blocking weighted budget: a request either reserves its
// bytes immediately or is refused — it never waits, so a burst cannot pile up
// an unbounded number of pending requests behind the budget.
type byteBudget struct {
	limit int64
	used  atomic.Int64
}

func (b *byteBudget) tryAcquire(n int64) bool {
	if n <= 0 {
		return true
	}
	if n > b.limit {
		n = b.limit // a single request larger than the budget may run alone
	}
	for {
		cur := b.used.Load()
		if cur != 0 && cur+n > b.limit {
			return false
		}
		if b.used.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func (b *byteBudget) release(n int64) {
	if n <= 0 {
		return
	}
	if n > b.limit {
		n = b.limit
	}
	b.used.Add(-n)
}

var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type latencyHist struct {
	buckets []uint64 // cumulative-by-construction counts per upper bound
	count   uint64
	sum     float64
}

type overloadState struct {
	upload      *byteBudget
	maxInflight int64
	inflight    atomic.Int64

	rejections map[string]*atomic.Uint64

	latencyMu sync.Mutex
	latency   map[string]*latencyHist

	admissionKindCounts func(context.Context) (map[string]int, error)
	dbStats             func() sql.DBStats
}

func newOverloadState(config Config) *overloadState {
	o := &overloadState{
		upload:              &byteBudget{limit: config.UploadMemoryBytes},
		maxInflight:         int64(config.MaxInflightRequests),
		rejections:          map[string]*atomic.Uint64{},
		latency:             map[string]*latencyHist{},
		admissionKindCounts: config.AdmissionKindCounts,
		dbStats:             config.DBStats,
	}
	for _, reason := range overloadReasons {
		o.rejections[reason] = &atomic.Uint64{}
	}
	return o
}

func (o *overloadState) reject(reason string) {
	if c, ok := o.rejections[reason]; ok {
		c.Add(1)
	}
}

func (o *overloadState) observeLatency(route string, d time.Duration) {
	seconds := d.Seconds()
	o.latencyMu.Lock()
	defer o.latencyMu.Unlock()
	h := o.latency[route]
	if h == nil {
		h = &latencyHist{buckets: make([]uint64, len(latencyBuckets))}
		o.latency[route] = h
	}
	h.count++
	h.sum += seconds
	for i, bound := range latencyBuckets {
		if seconds <= bound {
			h.buckets[i]++
		}
	}
}

// withInflightLimit bounds concurrent requests. Operator probes and
// long-lived event streams are exempt (a probe must still answer when the
// gateway is saturated; a stream is not request-rate work).
func (s *Server) withInflightLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, probe := probePaths[r.URL.Path]; probe || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			next.ServeHTTP(w, r)
			return
		}
		cur := s.overload.inflight.Add(1)
		defer s.overload.inflight.Add(-1)
		if s.overload.maxInflight > 0 && cur > s.overload.maxInflight {
			s.overload.reject("inflight_requests")
			s.writeError(w, r, http.StatusServiceUnavailable, apiError{
				Code:      "UBAG-OVERLOAD-REQUESTS-001",
				Category:  "overload",
				Message:   "the gateway is at its concurrent-request limit; retry shortly",
				Retryable: true,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// reserveUploadBytes reserves n bytes of the shared in-memory upload budget
// BEFORE the body is read. On refusal it writes the overload answer (native or
// OpenAI-facade shape) and reports ok=false; otherwise the returned func
// releases the reservation.
func (s *Server) reserveUploadBytes(w http.ResponseWriter, r *http.Request, n int64, facade bool) (release func(), ok bool) {
	if s.overload.upload.tryAcquire(n) {
		return func() { s.overload.upload.release(n) }, true
	}
	s.overload.reject("upload_memory")
	w.Header().Set("Retry-After", "2")
	if facade {
		s.writeJSON(w, http.StatusServiceUnavailable, openAIFacadeErrorEnvelope{Error: openAIFacadeError{
			Message: "the gateway is holding its maximum upload memory; retry shortly", Type: "server_error", Code: "overloaded", RetryAfterMS: 2000,
		}})
		return nil, false
	}
	s.writeError(w, r, http.StatusServiceUnavailable, apiError{
		Code:      "UBAG-OVERLOAD-UPLOAD-001",
		Category:  "overload",
		Message:   "the gateway is holding its maximum upload memory; retry shortly",
		Retryable: true,
	})
	return nil, false
}

// uploadReservation sizes a reservation from the declared Content-Length,
// falling back to the endpoint's hard limit when it is unknown.
func uploadReservation(r *http.Request, limit int64, factor int64) int64 {
	n := r.ContentLength
	if n <= 0 || n > limit {
		n = limit
	}
	return n * factor
}

// writeOverloadMetrics emits the admission / pressure series. Labels come from
// closed sets (reason, lane kind, route) so cardinality stays bounded.
func (s *Server) writeOverloadMetrics(w io.Writer) {
	o := s.overload
	fmt.Fprintf(w, "ubag_gateway_http_inflight_requests{service=\"ubag-gateway\",route=\"all\",method=\"all\"} %d\n", o.inflight.Load())
	for _, reason := range overloadReasons {
		fmt.Fprintf(w, "ubag_admission_rejections_total{reason=\"%s\"} %d\n", reason, o.rejections[reason].Load())
	}
	fmt.Fprintf(w, "ubag_upload_memory_inflight_bytes %d\n", o.upload.used.Load())
	fmt.Fprintf(w, "ubag_upload_memory_budget_bytes %d\n", o.upload.limit)

	o.latencyMu.Lock()
	routes := make([]string, 0, len(o.latency))
	for route := range o.latency {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	for _, route := range routes {
		h := o.latency[route]
		for i, bound := range latencyBuckets {
			fmt.Fprintf(w, "ubag_gateway_request_latency_seconds_bucket{route=\"%s\",le=\"%g\"} %d\n", promLabel(route), bound, h.buckets[i])
		}
		fmt.Fprintf(w, "ubag_gateway_request_latency_seconds_bucket{route=\"%s\",le=\"+Inf\"} %d\n", promLabel(route), h.count)
		fmt.Fprintf(w, "ubag_gateway_request_latency_seconds_sum{route=\"%s\"} %.6f\n", promLabel(route), h.sum)
		fmt.Fprintf(w, "ubag_gateway_request_latency_seconds_count{route=\"%s\"} %d\n", promLabel(route), h.count)
	}
	o.latencyMu.Unlock()

	if o.admissionKindCounts != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		counts, err := o.admissionKindCounts(ctx)
		cancel()
		if err == nil {
			kinds := make([]string, 0, len(counts))
			for kind := range counts {
				kinds = append(kinds, kind)
			}
			sort.Strings(kinds)
			for _, kind := range kinds {
				fmt.Fprintf(w, "ubag_admission_tokens_active{kind=\"%s\"} %d\n", promLabel(kind), counts[kind])
			}
		}
	}
	if o.dbStats != nil {
		st := o.dbStats()
		fmt.Fprintf(w, "ubag_db_pool_connections{state=\"open\"} %d\n", st.OpenConnections)
		fmt.Fprintf(w, "ubag_db_pool_connections{state=\"in_use\"} %d\n", st.InUse)
		fmt.Fprintf(w, "ubag_db_pool_connections{state=\"idle\"} %d\n", st.Idle)
		fmt.Fprintf(w, "ubag_db_pool_wait_total %d\n", st.WaitCount)
		fmt.Fprintf(w, "ubag_db_pool_wait_seconds_total %.6f\n", st.WaitDuration.Seconds())
	}
}
