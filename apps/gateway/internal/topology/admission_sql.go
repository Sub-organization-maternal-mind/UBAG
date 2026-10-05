package topology

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Lane is one admission budget a token counts against: a stable key and the
// ceiling for it. Dynamic lanes take their ceiling from the shared cap table
// (the worker-reported AIMD cap, written by whichever replica ingested the
// report) and fall back to Cap when none has been reported.
type Lane struct {
	Key     string
	Cap     int // <= 0 means unlimited
	Dynamic bool
}

// TokenBackend is the shared, atomic authority behind ConcurrencyRegistry for
// multi-replica deployments. A token counts against every lane it names; it is
// admitted only when ALL lanes have room, decided in one transaction with the
// lanes locked in key order, so concurrent replicas can never jointly exceed a
// ceiling. A token starts unassociated and expires after its TTL (a gateway
// that crashes between Acquire and job creation cannot leak capacity); once a
// job id is associated it is held until released (terminal paths and the
// stale-job reaper release it).
type TokenBackend interface {
	AcquireToken(ctx context.Context, lanes []Lane, ttl time.Duration, now time.Time) (tokenID string, ok bool, err error)
	AssociateToken(ctx context.Context, tokenID, jobID string) error
	// RenewToken extends an UNASSOCIATED token's expiry (a heartbeat). It
	// returns ErrTokenLost when the token no longer exists — expired and swept,
	// or released — which the holder must treat as loss of the lease.
	RenewToken(ctx context.Context, tokenID string, ttl time.Duration, now time.Time) error
	ReleaseToken(ctx context.Context, tokenID string) error
	ReleaseJobToken(ctx context.Context, jobID string) error
	PutLaneCap(ctx context.Context, laneKey string, cap int, now time.Time) error
	// SweepExpired deletes expired unassociated tokens and reports how many.
	SweepExpired(ctx context.Context, now time.Time) (int, error)
	// LaneKindCounts reports live tokens per lane kind (the lane key prefix
	// before the first ':') for low-cardinality metrics.
	LaneKindCounts(ctx context.Context, now time.Time) (map[string]int, error)
}

// SQLTokenBackend implements TokenBackend on SQLite or PostgreSQL. SQLite
// creates its own schema; PostgreSQL's comes from
// migrations/postgres/0021_admission_tokens.sql and Ready only asserts it.
type SQLTokenBackend struct {
	db       *sql.DB
	postgres bool
}

func NewSQLiteTokenBackend(db *sql.DB) *SQLTokenBackend { return &SQLTokenBackend{db: db} }
func NewPostgresTokenBackend(db *sql.DB) *SQLTokenBackend {
	return &SQLTokenBackend{db: db, postgres: true}
}

var sqliteAdmissionSchema = []string{
	`CREATE TABLE IF NOT EXISTS gateway_admission_tokens (
		token_id   TEXT PRIMARY KEY,
		job_id     TEXT NOT NULL DEFAULT '',
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_admission_tokens_job ON gateway_admission_tokens (job_id) WHERE job_id <> ''`,
	`CREATE TABLE IF NOT EXISTS gateway_admission_token_lanes (
		token_id TEXT NOT NULL,
		lane_key TEXT NOT NULL,
		PRIMARY KEY (token_id, lane_key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_admission_lanes_key ON gateway_admission_token_lanes (lane_key)`,
	`CREATE TABLE IF NOT EXISTS gateway_admission_caps (
		lane_key   TEXT PRIMARY KEY,
		cap        INTEGER NOT NULL,
		updated_at TEXT NOT NULL
	)`,
}

func (b *SQLTokenBackend) Ready(ctx context.Context) error {
	if b == nil || b.db == nil {
		return fmt.Errorf("admission: token backend is not configured")
	}
	if err := b.db.PingContext(ctx); err != nil {
		return err
	}
	if !b.postgres {
		for _, stmt := range sqliteAdmissionSchema {
			if _, err := b.db.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}
	for _, object := range []string{"gateway_admission_tokens", "gateway_admission_token_lanes", "gateway_admission_caps"} {
		var exists bool
		if err := b.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1)`, object).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("admission: postgres schema object %q is missing; apply migrations/postgres/0021_admission_tokens.sql", object)
		}
	}
	return nil
}

// ph renders the n-th (1-based) placeholder for the dialect.
func (b *SQLTokenBackend) ph(n int) string {
	if b.postgres {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// ts renders a time for the dialect: native on PostgreSQL, a fixed-width UTC
// string on SQLite so lexicographic order is chronological.
func (b *SQLTokenBackend) ts(t time.Time) any {
	if b.postgres {
		return t.UTC()
	}
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func newTokenID() string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return "tok_" + hex.EncodeToString(raw[:])
}

// liveCountSQL counts live tokens on one lane: associated tokens always, and
// unassociated ones until they expire. Args: lane_key, now.
func (b *SQLTokenBackend) liveCountSQL() string {
	return `SELECT COUNT(1) FROM gateway_admission_token_lanes l
JOIN gateway_admission_tokens t ON t.token_id = l.token_id
WHERE l.lane_key = ` + b.ph(1) + ` AND (t.job_id <> '' OR t.expires_at > ` + b.ph(2) + `)`
}

func (b *SQLTokenBackend) AcquireToken(ctx context.Context, lanes []Lane, ttl time.Duration, now time.Time) (string, bool, error) {
	if len(lanes) == 0 {
		return "", true, nil
	}
	sorted := append([]Lane(nil), lanes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, lane := range sorted {
		if b.postgres {
			// Serialize admissions per lane across every replica; lanes are
			// locked in key order so concurrent multi-lane acquires cannot
			// deadlock.
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "admission:"+lane.Key); err != nil {
				return "", false, err
			}
		}
		cap := lane.Cap
		if lane.Dynamic {
			var stored int
			err := tx.QueryRowContext(ctx, `SELECT cap FROM gateway_admission_caps WHERE lane_key = `+b.ph(1), lane.Key).Scan(&stored)
			if err == nil && stored > 0 {
				cap = stored
			} else if err != nil && err != sql.ErrNoRows {
				return "", false, err
			}
		}
		if cap <= 0 {
			continue
		}
		var live int
		if err := tx.QueryRowContext(ctx, b.liveCountSQL(), lane.Key, b.ts(now)).Scan(&live); err != nil {
			return "", false, err
		}
		if live >= cap {
			return "", false, nil
		}
	}
	id := newTokenID()
	if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_admission_tokens (token_id, job_id, expires_at, created_at) VALUES (`+
		b.ph(1)+`, '', `+b.ph(2)+`, `+b.ph(3)+`)`, id, b.ts(now.Add(ttl)), b.ts(now)); err != nil {
		return "", false, err
	}
	for _, lane := range sorted {
		if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_admission_token_lanes (token_id, lane_key) VALUES (`+b.ph(1)+`, `+b.ph(2)+`)`, id, lane.Key); err != nil {
			return "", false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (b *SQLTokenBackend) AssociateToken(ctx context.Context, tokenID, jobID string) error {
	res, err := b.db.ExecContext(ctx, `UPDATE gateway_admission_tokens SET job_id = `+b.ph(1)+` WHERE token_id = `+b.ph(2), jobID, tokenID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// The token expired and was swept before the job was created; the
		// job simply runs without a held token (permissive, never blocking).
		return nil
	}
	return nil
}

// ErrTokenLost reports that a lease token is gone: its holder no longer owns
// the resource and must stop.
var ErrTokenLost = errors.New("admission: token lost")

func (b *SQLTokenBackend) RenewToken(ctx context.Context, tokenID string, ttl time.Duration, now time.Time) error {
	res, err := b.db.ExecContext(ctx, `UPDATE gateway_admission_tokens SET expires_at = `+b.ph(1)+
		` WHERE token_id = `+b.ph(2)+` AND job_id = '' AND expires_at > `+b.ph(3), b.ts(now.Add(ttl)), tokenID, b.ts(now))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTokenLost
	}
	return nil
}

func (b *SQLTokenBackend) deleteTokens(ctx context.Context, where string, arg any) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_admission_token_lanes WHERE token_id IN (SELECT token_id FROM gateway_admission_tokens WHERE `+where+`)`, arg); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_admission_tokens WHERE `+where, arg); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *SQLTokenBackend) ReleaseToken(ctx context.Context, tokenID string) error {
	return b.deleteTokens(ctx, `token_id = `+b.ph(1), tokenID)
}

func (b *SQLTokenBackend) ReleaseJobToken(ctx context.Context, jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	return b.deleteTokens(ctx, `job_id = `+b.ph(1), jobID)
}

func (b *SQLTokenBackend) PutLaneCap(ctx context.Context, laneKey string, cap int, now time.Time) error {
	_, err := b.db.ExecContext(ctx, `INSERT INTO gateway_admission_caps (lane_key, cap, updated_at) VALUES (`+
		b.ph(1)+`, `+b.ph(2)+`, `+b.ph(3)+`) ON CONFLICT (lane_key) DO UPDATE SET cap = excluded.cap, updated_at = excluded.updated_at`,
		laneKey, cap, b.ts(now))
	return err
}

func (b *SQLTokenBackend) SweepExpired(ctx context.Context, now time.Time) (int, error) {
	where := `job_id = '' AND expires_at <= ` + b.ph(1)
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM gateway_admission_tokens WHERE `+where, b.ts(now)).Scan(&n); err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, nil
	}
	return n, b.deleteTokens(ctx, where, b.ts(now))
}

func (b *SQLTokenBackend) LaneKindCounts(ctx context.Context, now time.Time) (map[string]int, error) {
	rows, err := b.db.QueryContext(ctx, `SELECT l.lane_key FROM gateway_admission_token_lanes l
JOIN gateway_admission_tokens t ON t.token_id = l.token_id
WHERE t.job_id <> '' OR t.expires_at > `+b.ph(1), b.ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		kind, _, _ := strings.Cut(key, ":")
		out[kind]++
	}
	return out, rows.Err()
}
