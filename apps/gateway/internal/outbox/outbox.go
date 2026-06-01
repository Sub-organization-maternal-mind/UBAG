package outbox

import (
	"context"
	"time"
)

type Event struct {
	ID          string
	Topic       string
	Payload     []byte
	CreatedAt   time.Time
	PublishedAt *time.Time
}

type Store interface {
	Append(ctx context.Context, id, topic string, payload []byte) error
	MarkPublished(ctx context.Context, id string) error
	Pending(ctx context.Context, limit int) ([]Event, error)
	Ready(ctx context.Context) error
}
