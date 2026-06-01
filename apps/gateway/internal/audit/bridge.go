package audit

import (
	"context"

	"github.com/ubag/ubag/apps/gateway/internal/siem"
)

// BridgeStore wraps an audit Store and forwards every successfully appended
// record to a SIEM exporter for real-time streaming. Redaction is applied by
// the exporter's worker before delivery to sinks, so raw audit records are
// never sent directly to a sink.
//
// The notify func is called synchronously after a successful Append.
// Pass (*siem.Exporter).Enqueue to wire in the real exporter.
type BridgeStore struct {
	Store
	notify func(siem.Event)
}

// NewBridgeStore wraps store so that every successful Append forwards a copy
// of the appended record to notify. If notify is nil, Append panics on
// construction to fail fast and prevent a silent misconfiguration.
func NewBridgeStore(store Store, notify func(siem.Event)) *BridgeStore {
	if notify == nil {
		panic("audit: NewBridgeStore: notify must not be nil")
	}
	return &BridgeStore{Store: store, notify: notify}
}

// Append delegates to the wrapped store, then forwards the persisted record
// to the SIEM exporter. If the underlying Append fails, the event is not
// forwarded and the error is returned to the caller.
func (b *BridgeStore) Append(ctx context.Context, rec Record) (Record, error) {
	result, err := b.Store.Append(ctx, rec)
	if err != nil {
		return Record{}, err
	}
	b.notify(auditRecordToEvent(result))
	return result, nil
}

// auditRecordToEvent converts an audit Record to a siem.Event for forwarding.
func auditRecordToEvent(r Record) siem.Event {
	return siem.Event{
		ID:         r.ID,
		TenantID:   r.TenantID,
		AppID:      r.AppID,
		Type:       "audit",
		Actor:      r.Actor,
		Action:     r.Action,
		Resource:   r.Resource,
		Outcome:    r.Outcome,
		Timestamp:  r.OccurredAt,
		Attributes: cloneAttributes(r.Attributes),
	}
}

// cloneAttributes performs a shallow copy of attrs so the SIEM exporter's
// redaction pass cannot mutate the original audit record.
func cloneAttributes(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
