package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/ubag/ubag/apps/gateway/internal/retrypolicy"
)

const (
	defaultNATSWorkerDurable    = "ubag-worker"
	defaultNATSWorkerAckWait    = 30 * time.Second
	defaultNATSWorkerNakDelay   = time.Second
	defaultNATSWorkerMaxDeliver = 5
	defaultNATSWorkerFetchWait  = 500 * time.Millisecond
)

type NATSWorkerQueueConfig struct {
	URL        string
	StreamName string
	Subject    string
	// Region is the region segment used in the NATS subject filter.
	// When empty it defaults to "default", which matches subjects published
	// without an explicit region (single-region mode).
	Region     string
	Durable    string
	AckWait    time.Duration
	NakDelay   time.Duration
	FetchWait  time.Duration
	MaxDeliver int
}

// starvationRoundMask is the bitmask for forcing lower-priority lane checks
// every (1<<starvationRoundBits) rounds to prevent bulk/low starvation.
const starvationRoundBits = 4 // every 16th round

type NATSWorkerQueue struct {
	url        string
	streamName string
	subject    string
	region     string
	durable    string
	ackWait    time.Duration
	nakDelay   time.Duration
	fetchWait  time.Duration
	maxDeliver int

	mu        sync.Mutex
	conn      *nats.Conn
	js        jetstream.JetStream
	stream    jetstream.Stream
	consumers [5]jetstream.Consumer // indexed by priority lane: 0=crit 1=high 2=norm 3=low 4=bulk
	round     atomic.Uint64
}

func NewNATSWorkerQueue(config NATSWorkerQueueConfig) (*NATSWorkerQueue, error) {
	if config.URL == "" {
		config.URL = defaultNATSURL
	}
	if config.StreamName == "" {
		config.StreamName = defaultNATSStream
	}
	if config.Subject == "" {
		config.Subject = defaultNATSSubject
	}
	if config.Durable == "" {
		config.Durable = defaultNATSWorkerDurable
	}
	if config.AckWait <= 0 {
		config.AckWait = defaultNATSWorkerAckWait
	}
	if config.NakDelay <= 0 {
		config.NakDelay = defaultNATSWorkerNakDelay
	}
	if config.FetchWait <= 0 {
		config.FetchWait = defaultNATSWorkerFetchWait
	}
	if config.MaxDeliver <= 0 {
		config.MaxDeliver = defaultNATSWorkerMaxDeliver
	}
	if err := validateNATSSubjectBase(config.Subject); err != nil {
		return nil, err
	}
	if err := validateNATSName("stream", config.StreamName); err != nil {
		return nil, err
	}
	if err := validateNATSName("durable", config.Durable); err != nil {
		return nil, err
	}
	return &NATSWorkerQueue{
		url:        config.URL,
		streamName: config.StreamName,
		subject:    config.Subject,
		region:     config.Region,
		durable:    config.Durable,
		ackWait:    config.AckWait,
		nakDelay:   config.NakDelay,
		fetchWait:  config.FetchWait,
		maxDeliver: config.MaxDeliver,
	}, nil
}

func (q *NATSWorkerQueue) Ready(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureConnected(); err != nil {
		return fmt.Errorf("nats worker: connection failed: %w", err)
	}
	if err := q.ensureStream(ctx); err != nil {
		return fmt.Errorf("nats worker: stream setup failed: %w", err)
	}
	if err := q.ensureConsumer(ctx); err != nil {
		return fmt.Errorf("nats worker: consumer setup failed: %w", err)
	}
	return nil
}

func (q *NATSWorkerQueue) LeaseNext(ctx context.Context) (WorkerLease, bool, error) {
	if err := q.Ready(ctx); err != nil {
		return nil, false, err
	}

	q.mu.Lock()
	consumers := q.consumers
	q.mu.Unlock()

	// Determine polling order: normally high→low priority; every
	// (1<<starvationRoundBits) rounds reverse the order so that bulk/low
	// jobs are not starved when higher lanes are always busy.
	r := q.round.Add(1)
	order := [5]int{0, 1, 2, 3, 4} // crit first
	if r&(1<<starvationRoundBits-1) == 0 {
		order = [5]int{4, 3, 2, 1, 0} // bulk first on anti-starvation rounds
	}

	// Use a short per-lane fetch timeout so we cascade quickly to the next lane.
	shortWait := 5 * time.Millisecond
	for _, idx := range order {
		consumer := consumers[idx]
		if consumer == nil {
			continue
		}
		msg, err := consumer.Next(jetstream.FetchMaxWait(shortWait))
		if errors.Is(err, nats.ErrTimeout) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("nats worker: fetch failed: %w", err)
		}
		envelope, err := q.decodeMessage(msg)
		if err != nil {
			_ = terminateNATSMessage(msg, err.Error())
			return nil, true, nil
		}
		// §14.6 scheduling: if not_before is in the future, nack with computed delay.
		if envelope.NotBefore != nil && envelope.NotBefore.After(time.Now()) {
			delay := time.Until(*envelope.NotBefore)
			if delay > q.ackWait {
				delay = q.ackWait // cap at ack_wait to avoid exceeding the NATS deadline
			}
			_ = msg.NakWithDelay(delay)
			continue
		}
		return natsWorkerLease{queue: q, msg: msg, envelope: envelope}, true, nil
	}
	return nil, false, nil
}

func (q *NATSWorkerQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.conn != nil {
		if err := q.conn.Drain(); err != nil {
			q.conn.Close()
		}
		q.conn = nil
		q.js = nil
		q.stream = nil
		q.consumers = [5]jetstream.Consumer{}
	}
}

func (q *NATSWorkerQueue) ensureConnected() error {
	if q.conn != nil && q.conn.IsConnected() {
		return nil
	}
	if q.conn != nil {
		q.conn.Close()
		q.conn = nil
		q.js = nil
		q.stream = nil
		q.consumers = [5]jetstream.Consumer{}
	}
	conn, err := nats.Connect(
		q.url,
		nats.Name("ubag-worker-consumer"),
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
	q.conn = conn
	q.js = js
	return nil
}

func (q *NATSWorkerQueue) ensureStream(ctx context.Context) error {
	if q.stream != nil {
		return nil
	}
	cfg := jetstream.StreamConfig{
		Name:        q.streamName,
		Description: "UBAG gateway job dispatch queue",
		Subjects:    []string{q.subject + ".>"},
		Storage:     jetstream.FileStorage,
		Replicas:    1,
		MaxAge:      time.Duration(defaultNATSMaxAgeSecs) * time.Second,
		Duplicates:  10 * time.Minute,
	}
	stream, err := q.js.CreateOrUpdateStream(ctx, cfg)
	if err != nil {
		return err
	}
	q.stream = stream
	return nil
}

func (q *NATSWorkerQueue) ensureConsumer(ctx context.Context) error {
	if q.consumers[0] != nil {
		return nil
	}
	region := q.region
	if region == "" {
		region = "default"
	}
	for i, lane := range priorityLanes {
		name := q.durable + "-" + lane
		cfg := jetstream.ConsumerConfig{
			Name:          name,
			Durable:       name,
			Description:   "UBAG worker job consumer lane=" + lane,
			DeliverPolicy: jetstream.DeliverAllPolicy,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       q.ackWait,
			MaxDeliver:    q.maxDeliver,
			FilterSubject: q.subject + "." + region + "." + lane + ".*",
			ReplayPolicy:  jetstream.ReplayInstantPolicy,
		}
		consumer, err := q.stream.CreateOrUpdateConsumer(ctx, cfg)
		if err != nil {
			return err
		}
		q.consumers[i] = consumer
	}
	return nil
}

func (q *NATSWorkerQueue) decodeMessage(msg jetstream.Msg) (DispatchEnvelope, error) {
	if len(msg.Data()) > maxSpoolEnvelopeBytes {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: envelope exceeds %d bytes", maxSpoolEnvelopeBytes)
	}
	var envelope DispatchEnvelope
	if err := json.Unmarshal(msg.Data(), &envelope); err != nil {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: malformed envelope")
	}
	if strings.TrimSpace(envelope.JobID) == "" {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: envelope missing job_id")
	}
	// Accept both lane-prefixed format ({base}.{lane}.{jobID}) and the legacy
	// format ({base}.{jobID}) so messages published before the priority-lane
	// upgrade are still consumed correctly.
	_, jobIDFromSubject, ok := parseJobSubject(q.subject, msg.Subject())
	if !ok || jobIDFromSubject != envelope.JobID {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: subject %q does not match job subject", msg.Subject())
	}
	headers := msg.Headers()
	if headerJobID := strings.TrimSpace(headers.Get("Ubag-Job-Id")); headerJobID != "" && headerJobID != envelope.JobID {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: Ubag-Job-Id header does not match envelope")
	}
	if headerTenant := strings.TrimSpace(headers.Get("Ubag-Tenant-Id")); headerTenant != "" && headerTenant != envelope.TenantID {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: Ubag-Tenant-Id header does not match envelope")
	}
	if headerApp := strings.TrimSpace(headers.Get("Ubag-App-Id")); headerApp != "" && headerApp != envelope.AppID {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: Ubag-App-Id header does not match envelope")
	}
	return envelope, nil
}

// parseJobSubject parses a NATS dispatch subject and returns the lane and job ID.
// Accepts three formats:
//   - New region format: {base}.{region}.{lane}.{jobID}  →  3 segments after base; region detection: if part1 is not one of the 5 priorityLanes it is treated as a region segment
//   - Lane format: {base}.{lane}.{jobID}                 →  part1 is one of the 5 known lane names
//   - Legacy: {base}.{jobID}                             →  single token, treated as norm-priority job
//
// Returns ok=false if the subject does not match any expected format.
func parseJobSubject(base, subject string) (lane, jobID string, ok bool) {
	rest, found := strings.CutPrefix(subject, base+".")
	if !found {
		return "", "", false
	}

	parts := strings.SplitN(rest, ".", 3)
	switch len(parts) {
	case 3:
		// Could be {region}.{lane}.{jobID} or {lane}.{jobID}.{extra} (latter is invalid).
		// Detect by checking whether parts[0] is a known lane name.
		part0IsLane := false
		for _, l := range priorityLanes {
			if parts[0] == l {
				part0IsLane = true
				break
			}
		}
		if part0IsLane {
			// Old lane format with an unexpected extra segment — treat as invalid.
			return "", "", false
		}
		// parts[0] is the region segment; parts[1] must be a known lane; parts[2] is the jobID.
		laneCandidate := parts[1]
		for _, l := range priorityLanes {
			if laneCandidate == l {
				if jobIDCandidate := parts[2]; jobIDCandidate != "" && !strings.Contains(jobIDCandidate, ".") {
					return l, jobIDCandidate, true
				}
			}
		}
		return "", "", false

	case 2:
		// {lane}.{jobID} format (lane is one of the 5 known names)
		for _, l := range priorityLanes {
			if parts[0] == l {
				if parts[1] != "" {
					return l, parts[1], true
				}
			}
		}
		return "", "", false

	case 1:
		// Legacy format: single token, no dots — treated as norm-priority job
		if rest != "" {
			return "norm", rest, true
		}
		return "", "", false
	}

	return "", "", false
}

type natsWorkerLease struct {
	queue    *NATSWorkerQueue
	msg      jetstream.Msg
	envelope DispatchEnvelope
}

func (l natsWorkerLease) JobID() string {
	return l.envelope.JobID
}

func (l natsWorkerLease) LeaseID() string {
	metadata, err := l.msg.Metadata()
	if err != nil || metadata == nil {
		return "nats"
	}
	return fmt.Sprintf("%s:%s:%d:%d", metadata.Stream, metadata.Consumer, metadata.Sequence.Stream, metadata.Sequence.Consumer)
}

func (l natsWorkerLease) QueueName() string {
	if l.queue == nil {
		return defaultNATSStream
	}
	return l.queue.streamName
}

func (l natsWorkerLease) Envelope() DispatchEnvelope {
	return l.envelope
}

func (l natsWorkerLease) Complete(context.Context) error {
	return l.msg.Ack()
}

func (l natsWorkerLease) Fail(context.Context) error {
	return l.msg.Ack()
}

func (l natsWorkerLease) Cancel(context.Context) error {
	return l.msg.Ack()
}

func (l natsWorkerLease) Retry(context.Context) error {
	policy := retrypolicy.ParseFromMap(l.envelope.Job.Options)

	// Derive attempt count from NATS delivery metadata (NumDelivered - 1).
	retriesSoFar := 0
	if meta, err := l.msg.Metadata(); err == nil && meta != nil && meta.NumDelivered > 1 {
		retriesSoFar = int(meta.NumDelivered) - 1
	}

	delay := policy.NextDelay(retriesSoFar)
	if delay <= 0 {
		delay = defaultNATSWorkerNakDelay
	}
	return l.msg.NakWithDelay(delay)
}

func (l natsWorkerLease) Poison(_ context.Context, reason string) error {
	return terminateNATSMessage(l.msg, reason)
}

func terminateNATSMessage(msg jetstream.Msg, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "poison message"
	}
	if err := msg.TermWithReason(reason); err == nil {
		return nil
	}
	return msg.Term()
}

func validateNATSSubjectBase(subject string) error {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return fmt.Errorf("nats subject is required")
	}
	if strings.ContainsAny(subject, "*>") {
		return fmt.Errorf("nats subject %q must not contain wildcards", subject)
	}
	for _, token := range strings.Split(subject, ".") {
		if token == "" {
			return fmt.Errorf("nats subject %q contains an empty token", subject)
		}
		if hasInvalidNATSNameRune(token) {
			return fmt.Errorf("nats subject %q contains whitespace or a path separator", subject)
		}
	}
	return nil
}

func validateNATSName(kind string, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("nats %s name is required", kind)
	}
	if strings.ContainsAny(value, ".*>/\\") || hasInvalidNATSNameRune(value) {
		return fmt.Errorf("nats %s name %q is invalid", kind, value)
	}
	return nil
}

func hasInvalidNATSNameRune(value string) bool {
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\' {
			return true
		}
	}
	return false
}
