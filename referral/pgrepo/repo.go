// Package pgrepo implements referral.Repo against real Postgres — the
// production counterpart to referral.MemRepo.
package pgrepo

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/referral"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ referral.Repo = (*Repo)(nil)

func (r *Repo) TryClaim(ctx context.Context, accountID string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `INSERT INTO referral_claims (account_id) VALUES ($1) ON CONFLICT DO NOTHING`, accountID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RecordReward mirrors referral.MemRepo.RecordReward's contract exactly:
// it assigns r its ID by mutating the caller's struct (not just an
// internal copy), so ClaimCode's response carries the same ID
// ListByReferrer will later show.
func (r *Repo) RecordReward(ctx context.Context, rw *referral.Referral) error {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('referral_seq')`).Scan(&seq); err != nil {
		return err
	}
	rw.ID = "ref-" + strconv.FormatInt(seq, 10)
	if rw.CreatedAt.IsZero() {
		rw.CreatedAt = time.Now()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO referrals (id, referrer_id, referred_id, reward_coins, transaction_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		rw.ID, rw.ReferrerID, rw.ReferredID, rw.RewardCoins, rw.TransactionID, rw.CreatedAt)
	return err
}

func (r *Repo) ListByReferrer(ctx context.Context, referrerID string) ([]referral.Referral, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, referrer_id, referred_id, reward_coins, transaction_id, created_at
		FROM referrals WHERE referrer_id = $1 ORDER BY created_at DESC`, referrerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []referral.Referral
	for rows.Next() {
		var rw referral.Referral
		if err := rows.Scan(&rw.ID, &rw.ReferrerID, &rw.ReferredID, &rw.RewardCoins, &rw.TransactionID, &rw.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rw)
	}
	return out, rows.Err()
}
