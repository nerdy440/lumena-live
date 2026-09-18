// Package pgrepo implements rolloutsvc's FlagRepo and InternalRepo against
// real Postgres — the production counterpart to rollout.MemFlagRepo /
// MemInternalRepo. MetricsRepo has no Postgres implementation — see the
// 0011_rollout.sql migration's doc comment for why canary metrics stay
// in-process only.
package pgrepo

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/rollout"
)

// ─── Flags ──────────────────────────────────────────────────────────────────

type FlagRepo struct{ pool *pgxpool.Pool }

func NewFlagRepo(pool *pgxpool.Pool) *FlagRepo { return &FlagRepo{pool: pool} }

func (r *FlagRepo) Get(ctx context.Context, key string) (*rollout.FeatureFlag, error) {
	var f rollout.FeatureFlag
	var stage string
	err := r.pool.QueryRow(ctx, `SELECT key, stage, created_at, updated_at FROM rollout_flags WHERE key = $1`, key).
		Scan(&f.Key, &stage, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, rollout.ErrFlagNotFound
	}
	if err != nil {
		return nil, err
	}
	f.Stage = rollout.Stage(stage)
	return &f, nil
}

// Upsert creates the flag at whatever stage is given if it doesn't exist
// yet, or updates its stage if it does — matches MemFlagRepo.Upsert
// exactly (it never defaults a new flag to StageOff internally; the
// caller always passes the target stage explicitly).
func (r *FlagRepo) Upsert(ctx context.Context, key string, stage rollout.Stage) (*rollout.FeatureFlag, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO rollout_flags (key, stage, created_at, updated_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (key) DO UPDATE SET stage = $2, updated_at = now()
		RETURNING key, stage, created_at, updated_at`, key, string(stage))
	var f rollout.FeatureFlag
	var gotStage string
	if err := row.Scan(&f.Key, &gotStage, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return nil, err
	}
	f.Stage = rollout.Stage(gotStage)
	return &f, nil
}

func (r *FlagRepo) List(ctx context.Context) ([]rollout.FeatureFlag, error) {
	rows, err := r.pool.Query(ctx, `SELECT key, stage, created_at, updated_at FROM rollout_flags ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]rollout.FeatureFlag, 0)
	for rows.Next() {
		var f rollout.FeatureFlag
		var stage string
		if err := rows.Scan(&f.Key, &stage, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		f.Stage = rollout.Stage(stage)
		out = append(out, f)
	}
	return out, rows.Err()
}

// ─── Internal/staff allowlist ───────────────────────────────────────────────

type InternalRepo struct{ pool *pgxpool.Pool }

func NewInternalRepo(pool *pgxpool.Pool) *InternalRepo { return &InternalRepo{pool: pool} }

func (r *InternalRepo) IsInternal(ctx context.Context, accountID string) (bool, error) {
	var is bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM rollout_internal_accounts WHERE account_id = $1)`, accountID).Scan(&is)
	return is, err
}

func (r *InternalRepo) MarkInternal(ctx context.Context, accountID string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO rollout_internal_accounts (account_id) VALUES ($1) ON CONFLICT DO NOTHING`, accountID)
	return err
}
