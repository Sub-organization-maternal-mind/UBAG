package siem

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Stats is a point-in-time snapshot of Exporter counters.
//
//   - Enqueued: events accepted into the buffer.
//   - Exported: successful (sink, batch) deliveries, summed over sinks.
//   - Dropped:  events rejected because the buffer was full or closed.
//   - Failed:   events dead-lettered after exhausting retries for a sink,
//     summed over sinks. This doubles as the dead-letter count metric.
type Stats struct {
	Enqueued int
	Exported int
	Dropped  int
	Failed   int
}

// ExporterConfig configures an Exporter. All fields are optional except Sinks.
type ExporterConfig struct {
	// Sinks receive every redacted event. At least one is required.
	Sinks []Sink
	// BufferSize bounds the in-memory queue. Defaults to 1024.
	BufferSize int
	// BatchSize bounds events delivered to a sink per Export call. Defaults to 100.
	BatchSize int
	// MaxAttempts bounds export attempts per (sink, batch). Defaults to 5.
	MaxAttempts int
	// FlushInterval flushes a partial batch when idle. Defaults to time.Second.
	FlushInterval time.Duration
	// Backoff returns the delay before retry attempt n (1-based). Injectable
	// for deterministic tests. Defaults to exponential-ish growth capped at 5s.
	Backoff func(attempt int) time.Duration
	// Now is the clock used for timestamps. Defaults to time.Now.
	Now func() time.Time
}

// Exporter is a non-blocking, bounded fan-out of audit events to one or more
// sinks. Events are redacted before delivery. A single worker drains the
// buffer in batches and retries failed exports with bounded attempts, so
// ordering within a sink is preserved.
type Exporter struct {
	sinks         []Sink
	events        chan Event
	bufferSize    int
	batchSize     int
	maxAttempts   int
	flushInterval time.Duration
	backoff       func(attempt int) time.Duration
	now           func() time.Time

	quit       chan struct{}
	workerDone chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
	started    atomic.Bool

	enqueued atomic.Int64
	exported atomic.Int64
	dropped  atomic.Int64
	failed   atomic.Int64
}

// NewExporter constructs an Exporter. Call Start to launch the worker, then
// Enqueue events, and Close for a graceful drain.
func NewExporter(config ExporterConfig) (*Exporter, error) {
	if len(config.Sinks) == 0 {
		return nil, fmt.Errorf("siem: at least one sink is required")
	}
	bufferSize := config.BufferSize
	if bufferSize <= 0 {
		bufferSize = 1024
	}
	batchSize := config.BatchSize
	if batchSize <= 0 {
		batchSize = 100
	}
	maxAttempts := config.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	flushInterval := config.FlushInterval
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	backoff := config.Backoff
	if backoff == nil {
		backoff = defaultBackoff
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	sinks := make([]Sink, len(config.Sinks))
	copy(sinks, config.Sinks)
	return &Exporter{
		sinks:         sinks,
		events:        make(chan Event, bufferSize),
		bufferSize:    bufferSize,
		batchSize:     batchSize,
		maxAttempts:   maxAttempts,
		flushInterval: flushInterval,
		backoff:       backoff,
		now:           now,
		quit:          make(chan struct{}),
		workerDone:    make(chan struct{}),
	}, nil
}

func defaultBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 100 * time.Millisecond
	}
	delay := 100 * time.Millisecond << uint(attempt-1)
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

// Start launches the background worker. It is safe to call once; subsequent
// calls are no-ops.
func (e *Exporter) Start() {
	e.startOnce.Do(func() {
		e.started.Store(true)
		go e.run()
	})
}

// Enqueue offers an event to the buffer without blocking. If the buffer is
// full or the Exporter has begun closing, the event is dropped and counted.
func (e *Exporter) Enqueue(event Event) {
	select {
	case <-e.quit:
		e.dropped.Add(1)
		return
	default:
	}
	select {
	case e.events <- event:
		e.enqueued.Add(1)
	default:
		e.dropped.Add(1)
	}
}

// Stats returns a snapshot of the counters.
func (e *Exporter) Stats() Stats {
	return Stats{
		Enqueued: int(e.enqueued.Load()),
		Exported: int(e.exported.Load()),
		Dropped:  int(e.dropped.Load()),
		Failed:   int(e.failed.Load()),
	}
}

// Close stops accepting events, drains the buffer, and waits for the worker to
// finish or for ctx to expire. It is safe to call multiple times.
func (e *Exporter) Close(ctx context.Context) error {
	e.closeOnce.Do(func() {
		close(e.quit)
	})
	if !e.started.Load() {
		// No worker; drain synchronously so buffered events are not lost.
		e.drainBuffer(ctx)
		return ctx.Err()
	}
	select {
	case <-e.workerDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Exporter) run() {
	defer close(e.workerDone)
	batch := make([]Event, 0, e.batchSize)
	timer := time.NewTimer(e.flushInterval)
	defer timer.Stop()
	resetTimer := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(e.flushInterval)
	}
	for {
		select {
		case <-e.quit:
			if len(batch) > 0 {
				e.exportBatch(context.Background(), batch)
			}
			e.drainBuffer(context.Background())
			return
		case event := <-e.events:
			batch = append(batch, Redact(event))
			if len(batch) >= e.batchSize {
				e.exportBatch(context.Background(), batch)
				batch = batch[:0]
				resetTimer()
			}
		case <-timer.C:
			if len(batch) > 0 {
				e.exportBatch(context.Background(), batch)
				batch = batch[:0]
			}
			timer.Reset(e.flushInterval)
		}
	}
}

// drainBuffer flushes all currently-buffered events in batches. It is used on
// shutdown (and when no worker was started) so events are not lost.
func (e *Exporter) drainBuffer(ctx context.Context) {
	batch := make([]Event, 0, e.batchSize)
	for {
		select {
		case event := <-e.events:
			batch = append(batch, Redact(event))
			if len(batch) >= e.batchSize {
				e.exportBatch(ctx, batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				e.exportBatch(ctx, batch)
			}
			return
		}
	}
}

// exportBatch delivers a redacted batch to every sink with bounded retry.
func (e *Exporter) exportBatch(ctx context.Context, batch []Event) {
	if len(batch) == 0 {
		return
	}
	// Defensive copy so a sink cannot retain/mutate the worker's scratch slice.
	events := make([]Event, len(batch))
	copy(events, batch)
	for _, sink := range e.sinks {
		if e.exportToSink(ctx, sink, events) {
			e.exported.Add(int64(len(events)))
		} else {
			e.failed.Add(int64(len(events)))
		}
	}
}

// exportToSink attempts delivery to a single sink, retrying transient errors
// up to maxAttempts. It returns true on success.
func (e *Exporter) exportToSink(ctx context.Context, sink Sink, events []Event) bool {
	for attempt := 1; attempt <= e.maxAttempts; attempt++ {
		if err := sink.Export(ctx, events); err == nil {
			return true
		}
		if attempt == e.maxAttempts {
			break
		}
		if !sleepCtx(ctx, e.backoff(attempt)) {
			// Context cancelled mid-backoff; make a final immediate attempt so
			// graceful shutdown still tries to flush, then give up.
			return sink.Export(ctx, events) == nil
		}
	}
	return false
}

// sleepCtx sleeps for d unless ctx is cancelled first. It returns true if the
// full duration elapsed, false if interrupted.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
