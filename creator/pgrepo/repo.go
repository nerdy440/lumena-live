// Package pgrepo implements creatorsvc's KYCRepo, PayoutRepo, and
// PayoutProfileRepo against real Postgres — the production counterpart to
// creator.MemKYCRepo / MemPayoutRepo / MemPayoutProfileRepo.
package pgrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/creator"
)

// ─── KYC ────────────────────────────────────────────────────────────────────

type KYCRepo struct {
	pool *pgxpool.Pool
}

func NewKYCRepo(pool *pgxpool.Pool) *KYCRepo {
	return &KYCRepo{pool: pool}
}

func scanKYC(row pgx.Row) (*creator.KYCRecord, error) {
	var rec creator.KYCRecord
	var status string
	err := row.Scan(&rec.AccountID, &status, &rec.LegalName, &rec.Country, &rec.SubmittedAt, &rec.DecidedAt)
	if err != nil {
		return nil, err
	}
	rec.Status = creator.KYCStatus(status)
	return &rec, nil
}

func (r *KYCRepo) Get(ctx context.Context, accountID string) (*creator.KYCRecord, error) {
	row := r.pool.QueryRow(ctx, `SELECT account_id, status, legal_name, country, submitted_at, decided_at FROM creator_kyc WHERE account_id = $1`, accountID)
	rec, err := scanKYC(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return &creator.KYCRecord{AccountID: accountID, Status: creator.KYCNone}, nil
	}
	return rec, err
}

func (r *KYCRepo) Submit(ctx context.Context, accountID, legalName, country string) (*creator.KYCRecord, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO creator_kyc (account_id, status, legal_name, country, submitted_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (account_id) DO UPDATE SET
			status = $2, legal_name = $3, country = $4, submitted_at = now(), decided_at = NULL
		RETURNING account_id, status, legal_name, country, submitted_at, decided_at`,
		accountID, string(creator.KYCPending), legalName, country)
	return scanKYC(row)
}

func (r *KYCRepo) Decide(ctx context.Context, accountID string, status creator.KYCStatus) (*creator.KYCRecord, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO creator_kyc (account_id, status, submitted_at, decided_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (account_id) DO UPDATE SET status = $2, decided_at = now()
		RETURNING account_id, status, legal_name, country, submitted_at, decided_at`,
		accountID, string(status))
	return scanKYC(row)
}

// ─── Payouts ──────────────────────────────────────────────────────────────

type PayoutRepo struct {
	pool *pgxpool.Pool
}

func NewPayoutRepo(pool *pgxpool.Pool) *PayoutRepo {
	return &PayoutRepo{pool: pool}
}

const payoutCols = `id, account_id, amount_diamonds, status, failure_reason, transaction_id,
	reversal_transaction_id, payout_reference, admin_note, reviewed_by, requested_at, reviewed_at, paid_at, updated_at`

func scanPayout(row pgx.Row) (*creator.Payout, error) {
	var p creator.Payout
	var status string
	err := row.Scan(&p.ID, &p.AccountID, &p.AmountDiamonds, &status, &p.FailureReason, &p.TransactionID,
		&p.ReversalTransactionID, &p.PayoutReference, &p.AdminNote, &p.ReviewedBy, &p.RequestedAt, &p.ReviewedAt, &p.PaidAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, creator.ErrPayoutNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Status = creator.PayoutStatus(status)
	return &p, nil
}

func (r *PayoutRepo) Create(ctx context.Context, p creator.Payout) (*creator.Payout, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('creator_payout_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	id := fmt.Sprintf("payout-%04d", seq)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO creator_payouts (id, account_id, amount_diamonds, status, transaction_id, requested_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, now(), now())
		RETURNING `+payoutCols,
		id, p.AccountID, p.AmountDiamonds, string(creator.PayoutRequested), p.TransactionID)
	return scanPayout(row)
}

func (r *PayoutRepo) UpdateStatus(ctx context.Context, id string, status creator.PayoutStatus, failureReason string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE creator_payouts SET status = $2, failure_reason = $3, updated_at = now() WHERE id = $1`,
		id, string(status), failureReason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return creator.ErrPayoutNotFound
	}
	return nil
}

// Update applies mutate to the stored payout inside a single transaction
// with the row locked via SELECT ... FOR UPDATE — the real-database
// equivalent of MemPayoutRepo's mutex-held read-modify-write, giving the
// same atomicity guarantee doc rule 13 requires.
func (r *PayoutRepo) Update(ctx context.Context, id string, mutate func(*creator.Payout) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	p, err := scanPayout(tx.QueryRow(ctx, `SELECT `+payoutCols+` FROM creator_payouts WHERE id = $1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if err := mutate(p); err != nil {
		return err
	}
	p.UpdatedAt = time.Now()
	_, err = tx.Exec(ctx, `
		UPDATE creator_payouts SET
			status = $2, failure_reason = $3, transaction_id = $4, reversal_transaction_id = $5,
			payout_reference = $6, admin_note = $7, reviewed_by = $8, reviewed_at = $9, paid_at = $10, updated_at = $11
		WHERE id = $1`,
		p.ID, string(p.Status), p.FailureReason, p.TransactionID, p.ReversalTransactionID,
		p.PayoutReference, p.AdminNote, p.ReviewedBy, p.ReviewedAt, p.PaidAt, p.UpdatedAt)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *PayoutRepo) Get(ctx context.Context, id string) (*creator.Payout, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+payoutCols+` FROM creator_payouts WHERE id = $1`, id)
	return scanPayout(row)
}

func (r *PayoutRepo) ListByAccount(ctx context.Context, accountID string) ([]creator.Payout, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+payoutCols+` FROM creator_payouts WHERE account_id = $1 ORDER BY requested_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []creator.Payout
	for rows.Next() {
		p, err := scanPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *PayoutRepo) PendingOrProcessingTotal(ctx context.Context, accountID string) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount_diamonds), 0) FROM creator_payouts
		WHERE account_id = $1 AND status IN ('requested', 'approved', 'processing')`, accountID).Scan(&total)
	return total, err
}

func (r *PayoutRepo) CountPending(ctx context.Context) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM creator_payouts WHERE status IN ('requested', 'approved', 'processing')`).Scan(&n)
	return n, err
}

func (r *PayoutRepo) ListAll(ctx context.Context, statusFilter creator.PayoutStatus) ([]creator.Payout, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+payoutCols+` FROM creator_payouts
		WHERE $1 = '' OR status = $1
		ORDER BY requested_at DESC`, string(statusFilter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]creator.Payout, 0)
	for rows.Next() {
		p, err := scanPayout(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ─── Payout profile ─────────────────────────────────────────────────────────

type PayoutProfileRepo struct {
	pool *pgxpool.Pool
}

func NewPayoutProfileRepo(pool *pgxpool.Pool) *PayoutProfileRepo {
	return &PayoutProfileRepo{pool: pool}
}

func (r *PayoutProfileRepo) Get(ctx context.Context, accountID string) (*creator.PayoutProfile, error) {
	var p creator.PayoutProfile
	var method string
	err := r.pool.QueryRow(ctx, `
		SELECT account_id, method, country, payee_name, destination, updated_at
		FROM creator_payout_profiles WHERE account_id = $1`, accountID).
		Scan(&p.AccountID, &method, &p.Country, &p.PayeeName, &p.Destination, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &creator.PayoutProfile{AccountID: accountID}, nil
	}
	if err != nil {
		return nil, err
	}
	p.Method = creator.PayoutMethod(method)
	return &p, nil
}

func (r *PayoutProfileRepo) Set(ctx context.Context, p creator.PayoutProfile) (*creator.PayoutProfile, error) {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO creator_payout_profiles (account_id, method, country, payee_name, destination, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (account_id) DO UPDATE SET
			method = $2, country = $3, payee_name = $4, destination = $5, updated_at = now()`,
		p.AccountID, string(p.Method), p.Country, p.PayeeName, p.Destination)
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, p.AccountID)
}
