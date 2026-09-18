// Package pgrepo is the Postgres-backed implementation of authsvc.Repo —
// the real-database counterpart to auth/memrepo, wired in by
// feed/cmd/api when DATABASE_URL is set. Same interface, same behavior
// contract; see migrations/0001_auth.sql for the schema this reads and
// writes.
package pgrepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/auth/authsvc"
)

// hashHex re-encodes a token hash as hex before it touches Postgres.
// authsvc computes token hashes as raw SHA-256 bytes reinterpreted as a Go
// string (string([]byte) — see token.RefreshToken.Hash) — that's a valid
// Go string (any byte sequence is), but not valid UTF-8, which a Postgres
// TEXT column rejects outright. The in-memory repo never hits this because
// a Go map key has no such encoding constraint. Hex round-trips exactly
// and keeps CreateSession/GetSession/RotateSession's signatures matching
// authsvc.Repo unchanged — this is purely a storage-layer detail.
func hashHex(tokenHash string) string {
	return hex.EncodeToString([]byte(tokenHash))
}

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ authsvc.Repo = (*Repo)(nil)

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s-%x", prefix, b)
}

func (r *Repo) scanAccount(row pgx.Row) (*authsvc.Account, error) {
	var acc authsvc.Account
	var status, ageStatus string
	if err := row.Scan(&acc.ID, &acc.PhoneE164, &acc.Email, &status, &acc.RegionCode, &ageStatus, &acc.IsNewAccount, &acc.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	acc.Status = authsvc.AccountStatus(status)
	acc.AgeStatus = authsvc.AgeStatus(ageStatus)
	return &acc, nil
}

const accountCols = "id, phone_e164, email, status, region_code, age_status, is_new_account, created_at"

func (r *Repo) FindByPhone(ctx context.Context, phoneE164 string) (*authsvc.Account, error) {
	row := r.pool.QueryRow(ctx, "SELECT "+accountCols+" FROM accounts WHERE phone_e164 = $1", phoneE164)
	return r.scanAccount(row)
}

func (r *Repo) FindByEmail(ctx context.Context, email string) (*authsvc.Account, error) {
	row := r.pool.QueryRow(ctx, "SELECT "+accountCols+" FROM accounts WHERE email = $1", email)
	return r.scanAccount(row)
}

func (r *Repo) FindByID(ctx context.Context, id string) (*authsvc.Account, error) {
	row := r.pool.QueryRow(ctx, "SELECT "+accountCols+" FROM accounts WHERE id = $1", id)
	return r.scanAccount(row)
}

func (r *Repo) CreateAccount(ctx context.Context, phoneE164, email *string, region string) (*authsvc.Account, error) {
	id := newID("acc")
	now := time.Now()
	_, err := r.pool.Exec(ctx,
		`INSERT INTO accounts (id, phone_e164, email, status, region_code, age_status, is_new_account, created_at)
		 VALUES ($1, $2, $3, 'active', $4, 'undeclared', true, $5)`,
		id, phoneE164, email, region, now)
	if err != nil {
		return nil, fmt.Errorf("pgrepo: create account: %w", err)
	}
	return &authsvc.Account{
		ID: id, PhoneE164: phoneE164, Email: email, Status: authsvc.StatusActive,
		RegionCode: region, AgeStatus: authsvc.AgeUndeclared, IsNewAccount: true, CreatedAt: now,
	}, nil
}

func (r *Repo) GetPasswordHash(ctx context.Context, accountID string) (string, error) {
	var hash string
	err := r.pool.QueryRow(ctx, "SELECT hash FROM password_hashes WHERE account_id = $1", accountID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("not found")
	}
	return hash, err
}

func (r *Repo) SetPasswordHash(ctx context.Context, accountID, hash string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO password_hashes (account_id, hash) VALUES ($1, $2)
		 ON CONFLICT (account_id) DO UPDATE SET hash = EXCLUDED.hash`,
		accountID, hash)
	return err
}

func (r *Repo) CreateSession(ctx context.Context, accountID, deviceID, deviceLabel, tokenHash, ip string) (string, error) {
	id := newID("sess")
	_, err := r.pool.Exec(ctx,
		`INSERT INTO sessions (id, account_id, device_id, device_label, token_hash, ip) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, accountID, deviceID, deviceLabel, hashHex(tokenHash), ip)
	if err != nil {
		return "", fmt.Errorf("pgrepo: create session: %w", err)
	}
	return id, nil
}

func (r *Repo) GetSession(ctx context.Context, tokenHash string) (*authsvc.Session, error) {
	var s authsvc.Session
	var storedHash string
	err := r.pool.QueryRow(ctx,
		`SELECT id, account_id, device_id, token_hash, revoked_at FROM sessions WHERE token_hash = $1`,
		hashHex(tokenHash)).Scan(&s.ID, &s.AccountID, &s.DeviceID, &storedHash, &s.RevokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if decoded, decErr := hex.DecodeString(storedHash); decErr == nil {
		s.TokenHash = string(decoded)
	}
	return &s, nil
}

func (r *Repo) RotateSession(ctx context.Context, sessionID, newTokenHash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE sessions SET token_hash = $1, last_seen_at = now() WHERE id = $2`,
		hashHex(newTokenHash), sessionID)
	return err
}

func (r *Repo) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := r.pool.Exec(ctx, `UPDATE sessions SET revoked_at = now() WHERE id = $1`, sessionID)
	return err
}

func (r *Repo) RevokeAllDeviceSessions(ctx context.Context, accountID, deviceID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE account_id = $1 AND device_id = $2 AND revoked_at IS NULL`,
		accountID, deviceID)
	return err
}

func (r *Repo) DeclareAge(ctx context.Context, accountID string, dob time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE accounts SET date_of_birth = $1, age_status = 'declared' WHERE id = $2`,
		dob, accountID)
	return err
}

func (r *Repo) GetAgeStatus(ctx context.Context, accountID string) (authsvc.AgeStatus, *time.Time, error) {
	var status string
	var dob *time.Time
	err := r.pool.QueryRow(ctx, `SELECT age_status, date_of_birth FROM accounts WHERE id = $1`, accountID).Scan(&status, &dob)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	return authsvc.AgeStatus(status), dob, nil
}

func (r *Repo) SetAgeStatus(ctx context.Context, accountID string, status authsvc.AgeStatus) error {
	_, err := r.pool.Exec(ctx, `UPDATE accounts SET age_status = $1 WHERE id = $2`, string(status), accountID)
	return err
}

func (r *Repo) SetAccountStatus(ctx context.Context, accountID string, status authsvc.AccountStatus) error {
	_, err := r.pool.Exec(ctx, `UPDATE accounts SET status = $1 WHERE id = $2`, string(status), accountID)
	return err
}

func (r *Repo) MarkAccountDeleted(ctx context.Context, accountID string) error {
	return r.SetAccountStatus(ctx, accountID, authsvc.StatusDeleted)
}

// CancelDeletion mirrors memrepo's exact semantics: only clears the
// deleted status if the account is currently deleted (a no-op otherwise,
// not an error) — see auth/memrepo/repo.go's CancelDeletion.
func (r *Repo) CancelDeletion(ctx context.Context, accountID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE accounts SET status = 'active' WHERE id = $1 AND status = 'deleted'`,
		accountID)
	return err
}

// IsUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505) — exported so callers that need to
// distinguish "already exists" from other failures can do so without
// importing pgconn themselves.
func IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}
