package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
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
	Durable    string
	AckWait    time.Duration
	NakDelay   time.Duration
	FetchWait  time.Duration
	MaxDeliver int
}

type NATSWorkerQueue struct {
	url        string
	streamName string
	subject    string
	durable    string
	ackWait    time.Duration
	nakDelay   time.Duration
	fetchWait  time.Duration
	maxDeliver int

	mu       sync.Mutex
	conn     *nats.Conn
	js       jetstream.JetStream
	stream   jetstream.Stream
	consumer jetstream.Consumer
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
	consumer := q.consumer
	q.mu.Unlock()
	msg, err := consumer.Next(jetstream.FetchMaxWait(q.fetchWait))
	if errors.Is(err, nats.ErrTimeout) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("nats worker: fetch failed: %w", err)
	}

	envelope, err := q.decodeMessage(msg)
	if err != nil {
		_ = terminateNATSMessage(msg, err.Error())
		return nil, true, nil
	}
	return natsWorkerLease{queue: q, msg: msg, envelope: envelope}, true, nil
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
		q.consumer = nil
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
		q.consumer = nil
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
	if q.consumer != nil {
		return nil
	}
	cfg := jetstream.ConsumerConfig{
		Name:          q.durable,
		Durable:       q.durable,
		Description:   "UBAG worker job consumer",
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       q.ackWait,
		MaxDeliver:    q.maxDeliver,
		FilterSubject: q.subject + ".*",
		ReplayPolicy:  jetstream.ReplayInstantPolicy,
	}
	consumer, err := q.stream.CreateOrUpdateConsumer(ctx, cfg)
	if err != nil {
		return err
	}
	q.consumer = consumer
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
	expectedSubject := q.subject + "." + envelope.JobID
	if msg.Subject() != expectedSubject {
		return DispatchEnvelope{}, fmt.Errorf("nats worker: subject %q does not match job subject %q", msg.Subject(), expectedSubject)
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
	delay := defaultNATSWorkerNakDelay
	if l.queue != nil && l.queue.nakDelay > 0 {
		delay = l.queue.nakDelay
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
