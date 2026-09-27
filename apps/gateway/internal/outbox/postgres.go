package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/storekit"
)

// gatewayOutboxEventsTable is created by the migration chain
// (migrations/postgres/0017_gateway_outbox_events.sql). The store asserts its
// existence instead of creating it at runtime, like every other Postgres
// store: Ready() failing closed at boot is louder and safer than silently
// self-provisioning a table the migration ledger does not know about.
const gatewayOutboxEventsTable = "gateway_outbox_events"

type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db, now: time.Now}
}

func (p *PostgresStore) Ready(ctx context.Context) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("outbox: postgres store is not configured")
	}
	if err := p.db.PingContext(ctx); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	if err := storekit.RequirePostgresObject(ctx, p.db, gatewayOutboxEventsTable); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}

func (p *PostgresStore) Append(ctx context.Context, id, topic string, payload []byte) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("outbox: postgres store is not configured")
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO gateway_outbox_events (id, topic, payload, created_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO NOTHING`,
		id, topic, payload, p.now().UTC())
	if err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	return nil
}
