package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestNewNATSWorkerQueueValidatesConfig(t *testing.T) {
	if _, err := NewNATSWorkerQueue(NATSWorkerQueueConfig{Subject: "ubag.jobs.*"}); err == nil {
		t.Fatal("NewNATSWorkerQueue accepted wildcard subject")
	}
	if _, err := NewNATSWorkerQueue(NATSWorkerQueueConfig{Durable: "ubag.worker"}); err == nil {
		t.Fatal("NewNATSWorkerQueue accepted durable with dot")
	}
	queue, err := NewNATSWorkerQueue(NATSWorkerQueueConfig{
		Subject:    "ubag.jobs",
		Durable:    "ubag_worker",
		AckWait:    time.Minute,
		NakDelay:   2 * time.Second,
		FetchWait:  time.Second,
		MaxDeliver: 7,
	})
	if err != nil {
		t.Fatalf("NewNATSWorkerQueue returned error: %v", err)
	}
	if queue.durable != "ubag_worker" || queue.maxDeliver != 7 {
		t.Fatalf("queue config not applied: %#v", queue)
	}
}

func TestNATSWorkerQueueDecodeMessageValidatesSubjectAndHeaders(t *testing.T) {
	queue, err := NewNATSWorkerQueue(NATSWorkerQueueConfig{Subject: "ubag.jobs"})
	if err != nil {
		t.Fatalf("NewNATSWorkerQueue returned error: %v", err)
	}
	payload := []byte(`{"api_version":"2026-05-22","job_id":"job_123","tenant_id":"tenant_a","app_id":"app_a","trace_id":"trace_123","job":{"target":"mock","command_type":"submit"}}`)

	// New lane-prefixed format (priority lane "norm").
	msg := &fakeNATSMsg{
		subject: "ubag.jobs.norm.job_123",
		data:    payload,
		headers: nats.Header{
			"Ubag-Job-Id":    []string{"job_123"},
			"Ubag-Tenant-Id": []string{"tenant_a"},
			"Ubag-App-Id":    []string{"app_a"},
		},
	}
	envelope, err := queue.decodeMessage(msg)
	if err != nil {
		t.Fatalf("decodeMessage (lane format) returned error: %v", err)
	}
	if envelope.JobID != "job_123" {
		t.Fatalf("job_id = %q", envelope.JobID)
	}

	// Legacy format (no lane prefix) must still be accepted.
	legacyMsg := &fakeNATSMsg{
		subject: "ubag.jobs.job_123",
		data:    payload,
		headers: nats.Header{
			"Ubag-Job-Id":    []string{"job_123"},
			"Ubag-Tenant-Id": []string{"tenant_a"},
			"Ubag-App-Id":    []string{"app_a"},
		},
	}
	if _, err := queue.decodeMessage(legacyMsg); err != nil {
		t.Fatalf("decodeMessage (legacy format) returned error: %v", err)
	}

	// Cancel subject is not a valid dispatch subject.
	msg.subject = "ubag.jobs.cancel.job_123"
	if _, err := queue.decodeMessage(msg); err == nil || !strings.Contains(err.Error(), "subject") {
		t.Fatalf("decodeMessage cancel subject error = %v, want subject mismatch", err)
	}
	// Tenant header mismatch must be caught.
	msg.subject = "ubag.jobs.norm.job_123"
	msg.headers.Set("Ubag-Tenant-Id", "tenant_b")
	if _, err := queue.decodeMessage(msg); err == nil || !strings.Contains(err.Error(), "Tenant") {
		t.Fatalf("decodeMessage tenant header error = %v, want tenant mismatch", err)
	}
}

func TestNATSWorkerLeaseAckLifecycle(t *testing.T) {
	msg := &fakeNATSMsg{}
	queue, err := NewNATSWorkerQueue(NATSWorkerQueueConfig{NakDelay: 25 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewNATSWorkerQueue returned error: %v", err)
	}
	lease := natsWorkerLease{queue: queue, msg: msg, envelope: DispatchEnvelope{JobID: "job_ack"}}
	if err := lease.Complete(context.Background()); err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if msg.acked != 1 {
		t.Fatalf("acked = %d, want 1", msg.acked)
	}
	if err := lease.Retry(context.Background()); err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	// Retry now uses retry policy backoff; just verify a positive delay was set.
	if msg.nakDelay <= 0 {
		t.Fatalf("nak delay = %s, want > 0 (policy-computed backoff)", msg.nakDelay)
	}
	if err := lease.Poison(context.Background(), "bad envelope"); err != nil {
		t.Fatalf("Poison returned error: %v", err)
	}
	if msg.termReason != "bad envelope" {
		t.Fatalf("term reason = %q", msg.termReason)
	}
}

type fakeNATSMsg struct {
	subject    string
	data       []byte
	headers    nats.Header
	acked      int
	nakDelay   time.Duration
	termReason string
}

func (m *fakeNATSMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{
		Stream:   "UBAG_JOBS",
		Consumer: "ubag-worker",
		Sequence: jetstream.SequencePair{
			Stream:   10,
			Consumer: 3,
		},
	}, nil
}

func (m *fakeNATSMsg) Data() []byte {
	return m.data
}

func (m *fakeNATSMsg) Headers() nats.Header {
	if m.headers == nil {
		return nats.Header{}
	}
	return m.headers
}

func (m *fakeNATSMsg) Subject() string {
	return m.subject
}

func (m *fakeNATSMsg) Reply() string {
	return ""
}

func (m *fakeNATSMsg) Ack() error {
	m.acked++
	return nil
}

func (m *fakeNATSMsg) DoubleAck(context.Context) error {
	m.acked++
	return nil
}

func (m *fakeNATSMsg) Nak() error {
	m.nakDelay = 0
	return nil
}

func (m *fakeNATSMsg) NakWithDelay(delay time.Duration) error {
	m.nakDelay = delay
	return nil
}

func (m *fakeNATSMsg) InProgress() error {
	return nil
}

func (m *fakeNATSMsg) Term() error {
	m.termReason = "terminated"
	return nil
}

func (m *fakeNATSMsg) TermWithReason(reason string) error {
	m.termReason = reason
	return nil
}
