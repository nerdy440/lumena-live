// Package pgrepo implements pv.Repo against real Postgres — the
// production counterpart to pv.MemRepo.
package pgrepo

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/pv"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ pv.Repo = (*Repo)(nil)

const sessCols = "id, caller_id, callee_id, state, rate_per_min_coins, created_at, accepted_at, ended_at, consumed_coins, hold_transaction_id"

func scanSession(row pgx.Row) (*pv.Session, error) {
	var s pv.Session
	var state string
	var acceptedAt, endedAt *time.Time
	err := row.Scan(&s.ID, &s.CallerID, &s.CalleeID, &state, &s.RatePerMinCoins, &s.CreatedAt,
		&acceptedAt, &endedAt, &s.ConsumedCoins, &s.HoldTransactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // pv.Repo.Get returns (nil, nil) on not-found — see pv.MemRepo.Get
	}
	if err != nil {
		return nil, err
	}
	s.State = pv.State(state)
	s.AcceptedAt = acceptedAt
	s.EndedAt = endedAt
	return &s, nil
}

func (r *Repo) Create(ctx context.Context, s *pv.Session) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO pv_sessions (id, caller_id, callee_id, state, rate_per_min_coins, created_at, accepted_at, ended_at, consumed_coins, hold_transaction_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		s.ID, s.CallerID, s.CalleeID, string(s.State), s.RatePerMinCoins, s.CreatedAt,
		s.AcceptedAt, s.EndedAt, s.ConsumedCoins, s.HoldTransactionID)
	return err
}

func (r *Repo) Get(ctx context.Context, id string) (*pv.Session, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+sessCols+` FROM pv_sessions WHERE id = $1`, id)
	return scanSession(row)
}

func (r *Repo) Update(ctx context.Context, s *pv.Session) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE pv_sessions SET
			state = $2, accepted_at = $3, ended_at = $4, consumed_coins = $5, hold_transaction_id = $6
		WHERE id = $1`,
		s.ID, string(s.State), s.AcceptedAt, s.EndedAt, s.ConsumedCoins, s.HoldTransactionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pv.ErrSessionNotFound
	}
	return nil
}

func (r *Repo) ListByAccount(ctx context.Context, accountID string) ([]pv.Session, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessCols+` FROM pv_sessions WHERE caller_id = $1 OR callee_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pv.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		if s != nil {
			out = append(out, *s)
		}
	}
	return out, rows.Err()
}
