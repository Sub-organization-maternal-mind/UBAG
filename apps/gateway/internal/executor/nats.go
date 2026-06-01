package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const (
	defaultNATSURL        = "nats://127.0.0.1:4222"
	defaultNATSStream     = "UBAG_JOBS"
	defaultNATSSubject    = "ubag.jobs"
	defaultNATSMaxAgeSecs = 86400 // 24 h
)

// priorityLanes are the §14.4 NATS subject lane names in descending priority.
var priorityLanes = [5]string{"crit", "high", "norm", "low", "bulk"}

// laneFromPriority maps a job options.priority string to its lane name.
func laneFromPriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "critical", "crit":
		return "crit"
	case "high":
		return "high"
	case "low":
		return "low"
	case "bulk", "background":
		return "bulk"
	default:
		return "norm"
	}
}

// laneSubject returns the NATS subject for a job in a given lane and region.
// Format: {base}.{region}.{lane}.{jobID}
func laneSubject(base, region, lane, jobID string) string {
	return base + "." + region + "." + lane + "." + jobID
}

type regionContextKey struct{}

// WithDispatchRegion returns ctx with the dispatch region set.
func WithDispatchRegion(ctx context.Context, region string) context.Context {
	return context.WithValue(ctx, regionContextKey{}, region)
}

// dispatchRegionFromContext extracts the dispatch region; defaults to "default".
func dispatchRegionFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(regionContextKey{}).(string); ok && v != "" {
		return v
	}
	return "default"
}

// NATSDispatcher publishes gateway-stamped job envelopes to a NATS JetStream
// stream and listens for cancellation notices.
//
// Environment variables consumed by NewNATSDispatcherFromEnv:
//
//	UBAG_NATS_URL     – NATS server URL   (default nats://127.0.0.1:4222)
//	UBAG_NATS_STREAM  – JetStream stream  (default UBAG_JOBS)
//	UBAG_NATS_SUBJECT – Subject prefix    (default ubag.jobs)
//
// Job envelopes are published to: <subject>.<jobID>
// Cancellation notices are published to: <subject>.cancel.<jobID>
type NATSDispatcher struct {
	url        string
	streamName string
	subject    string

	mu     sync.Mutex
	conn   *nats.Conn
	js     jetstream.JetStream
	stream jetstream.Stream
}

func NewNATSDispatcher(url, streamName, subject string) *NATSDispatcher {
	if url == "" {
		url = defaultNATSURL
	}
	if streamName == "" {
		streamName = defaultNATSStream
	}
	if subject == "" {
		subject = defaultNATSSubject
	}
	return &NATSDispatcher{
		url:        url,
		streamName: streamName,
		subject:    subject,
	}
}

// Ready connects to NATS (if not already connected) and ensures the JetStream
// stream exists with the configured subject filter.
func (d *NATSDispatcher) Ready(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := d.ensureConnected(); err != nil {
		return fmt.Errorf("nats: connection failed: %w", err)
	}
	if err := d.ensureStream(ctx); err != nil {
		return fmt.Errorf("nats: stream setup failed: %w", err)
	}
	return nil
}

// EnqueueJob publishes the gateway-stamped envelope to JetStream.
func (d *NATSDispatcher) EnqueueJob(ctx context.Context, job jobstore.Job) (Receipt, error) {
	d.mu.Lock()
	if err := d.ensureConnected(); err != nil {
		d.mu.Unlock()
		return Receipt{}, fmt.Errorf("nats: connection failed: %w", err)
	}
	if err := d.ensureStream(ctx); err != nil {
		d.mu.Unlock()
		return Receipt{}, fmt.Errorf("nats: stream setup failed: %w", err)
	}
	js := d.js
	d.mu.Unlock()

	if jobstore.TerminalStatus(job.Status) {
		return d.receipt(job.ID, 0), nil
	}

	envelope := EnvelopeFromJob(job)
	payload, err := json.Marshal(envelope)
	if err != nil {
		return Receipt{}, fmt.Errorf("nats: envelope marshal failed: %w", err)
	}

	opts := parseJobOptions(job.Options)
	lane := laneFromPriority(opts.Priority)
	region := dispatchRegionFromContext(ctx)
	subject := laneSubject(d.subject, region, lane, job.ID)
	msg := &nats.Msg{
		Subject: subject,
		Data:    payload,
		Header:  nats.Header{},
	}
	msg.Header.Set("Ubag-Job-Id", job.ID)
	msg.Header.Set("Ubag-Tenant-Id", job.TenantID)
	msg.Header.Set("Ubag-App-Id", job.AppID)
	msg.Header.Set("Ubag-Lane", lane)
	msg.Header.Set("Nats-Msg-Id", job.ID) // deduplication by job ID

	ack, err := js.PublishMsg(ctx, msg)
	if err != nil {
		return Receipt{}, fmt.Errorf("nats: publish failed: %w", err)
	}

	return d.receipt(job.ID, ack.Sequence), nil
}

// CancelJob publishes a cancellation notice for the job.
func (d *NATSDispatcher) CancelJob(ctx context.Context, job jobstore.Job, reason string) error {
	d.mu.Lock()
	if err := d.ensureConnected(); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("nats: connection failed: %w", err)
	}
	if err := d.ensureStream(ctx); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("nats: stream setup failed: %w", err)
	}
	js := d.js
	d.mu.Unlock()

	notice := map[string]any{
		"job_id":       job.ID,
		"api_version":  job.APIVersion,
		"tenant_id":    job.TenantID,
		"app_id":       job.AppID,
		"reason":       strings.TrimSpace(reason),
		"cancelled_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := json.Marshal(notice)
	if err != nil {
		return fmt.Errorf("nats: cancel marshal failed: %w", err)
	}

	subject := d.subject + ".cancel." + job.ID
	msg := &nats.Msg{
		Subject: subject,
		Data:    payload,
		Header:  nats.Header{},
	}
	msg.Header.Set("Ubag-Job-Id", job.ID)
	msg.Header.Set("Ubag-Tenant-Id", job.TenantID)
	msg.Header.Set("Ubag-App-Id", job.AppID)
	msg.Header.Set("Nats-Msg-Id", "cancel:"+job.ID)
	_, err = js.PublishMsg(ctx, msg)
	return err
}

// Stats returns JetStream stream depth.
func (d *NATSDispatcher) Stats(ctx context.Context) (Stats, error) {
	d.mu.Lock()
	stream := d.stream
	d.mu.Unlock()

	if stream == nil {
		return Stats{
			QueueName:        d.streamName,
			DepthByState:     map[string]int{"queued": 0},
			OldestAgeByState: map[string]time.Duration{"queued": 0},
		}, nil
	}

	info, err := stream.Info(ctx)
	if err != nil {
		return Stats{}, fmt.Errorf("nats: stream info failed: %w", err)
	}

	queued := int(info.State.Msgs)
	return Stats{
		QueueName:        d.streamName,
		DepthByState:     map[string]int{"queued": queued},
		OldestAgeByState: map[string]time.Duration{"queued": 0},
	}, nil
}

// Close drains and closes the NATS connection gracefully.
func (d *NATSDispatcher) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn != nil {
		if err := d.conn.Drain(); err != nil {
			d.conn.Close()
		}
		d.conn = nil
		d.js = nil
		d.stream = nil
	}
}

// ensureConnected connects to NATS if not already connected.
// Caller must hold d.mu.
func (d *NATSDispatcher) ensureConnected() error {
	if d.conn != nil && d.conn.IsConnected() {
		return nil
	}
	if d.conn != nil {
		d.conn.Close()
		d.conn = nil
		d.js = nil
		d.stream = nil
	}
	conn, err := nats.Connect(
		d.url,
		nats.Name("ubag-gateway"),
		nats.MaxReconnects(5),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		return err
	}
	js, err := jetstream.New(conn)
	if err != nil {
		_ = conn.Drain()
		return err
	}
	d.conn = conn
	d.js = js
	d.stream = nil
	return nil
}

// ensureStream creates or attaches to the JetStream stream.
// Caller must hold d.mu.
func (d *NATSDispatcher) ensureStream(ctx context.Context) error {
	if d.stream != nil {
		return nil
	}
	cfg := jetstream.StreamConfig{
		Name:        d.streamName,
		Description: "UBAG gateway job dispatch queue",
		Subjects:    []string{d.subject + ".>"},
		Storage:     jetstream.FileStorage,
		Replicas:    1,
		MaxAge:      time.Duration(defaultNATSMaxAgeSecs) * time.Second,
		Duplicates:  10 * time.Minute,
	}
	stream, err := d.js.CreateOrUpdateStream(ctx, cfg)
	if err != nil {
		return err
	}
	d.stream = stream
	return nil
}

func (d *NATSDispatcher) receipt(jobID string, seq uint64) Receipt {
	msgID := jobID
	if seq > 0 {
		msgID = fmt.Sprintf("%s@%d", jobID, seq)
	}
	return Receipt{
		Backend:    "nats",
		QueueName:  d.streamName,
		MessageID:  msgID,
		EnqueuedAt: time.Now().UTC(),
	}
}
