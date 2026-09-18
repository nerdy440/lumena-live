// Package pgrepo implements fraudsvc's DeviceRepo and DisputeRepo against
// real Postgres — the production counterpart to fraud.MemDeviceRepo /
// MemDisputeRepo.
package pgrepo

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ─── Device fingerprints ────────────────────────────────────────────────────

type DeviceRepo struct{ pool *pgxpool.Pool }

func NewDeviceRepo(pool *pgxpool.Pool) *DeviceRepo { return &DeviceRepo{pool: pool} }

// Record upserts a fingerprint row and returns every account currently
// associated with deviceHash (including accountID itself) — matches
// fraud.MemDeviceRepo.Record exactly.
func (r *DeviceRepo) Record(ctx context.Context, accountID, deviceHash string) ([]string, error) {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO fraud_device_fingerprints (account_id, device_hash, first_seen, last_seen)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (device_hash, account_id) DO UPDATE SET last_seen = now()`,
		accountID, deviceHash)
	if err != nil {
		return nil, err
	}
	return r.AccountsForDevice(ctx, deviceHash)
}

func (r *DeviceRepo) AccountsForDevice(ctx context.Context, deviceHash string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT account_id FROM fraud_device_fingerprints WHERE device_hash = $1`, deviceHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ─── Disputes ─────────────────────────────────────────────────────────────

type DisputeRepo struct{ pool *pgxpool.Pool }

func NewDisputeRepo(pool *pgxpool.Pool) *DisputeRepo { return &DisputeRepo{pool: pool} }

func (r *DisputeRepo) Increment(ctx context.Context, accountID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		INSERT INTO fraud_disputes (account_id, count) VALUES ($1, 1)
		ON CONFLICT (account_id) DO UPDATE SET count = fraud_disputes.count + 1
		RETURNING count`, accountID).Scan(&count)
	return count, err
}
