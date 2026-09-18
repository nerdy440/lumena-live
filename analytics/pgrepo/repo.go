// Package pgrepo implements analyticssvc.EventRepo against real Postgres —
// the production counterpart to analytics.MemEventRepo.
package pgrepo

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/analytics"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) Append(ctx context.Context, accountID string, eventType analytics.EventType) (*analytics.Event, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('analytics_event_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	e := analytics.Event{ID: fmt.Sprintf("evt-%06d", seq), Type: eventType, AccountID: accountID}
	if err := r.pool.QueryRow(ctx, `
		INSERT INTO analytics_events (id, type, account_id, created_at)
		VALUES ($1, $2, $3, now())
		RETURNING created_at`, e.ID, string(e.Type), e.AccountID).Scan(&e.CreatedAt); err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *Repo) ListSince(ctx context.Context, since time.Time) ([]analytics.Event, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, type, account_id, created_at FROM analytics_events
		WHERE created_at >= $1 ORDER BY created_at`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

func (r *Repo) ListByType(ctx context.Context, eventType analytics.EventType) ([]analytics.Event, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, type, account_id, created_at FROM analytics_events
		WHERE type = $1 ORDER BY created_at`, string(eventType))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows)
}

type rowsScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanEvents(rows rowsScanner) ([]analytics.Event, error) {
	var out []analytics.Event
	for rows.Next() {
		var e analytics.Event
		var t string
		if err := rows.Scan(&e.ID, &t, &e.AccountID, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Type = analytics.EventType(t)
		out = append(out, e)
	}
	return out, rows.Err()
}
