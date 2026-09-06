package outbox

import (
	"context"
	"time"
)

type Event struct {
	ID        string
	Topic     string
	Payload   []byte
	CreatedAt time.Time
}

type Store interface {
	Append(ctx context.Context, id, topic string, payload []byte) error
}
