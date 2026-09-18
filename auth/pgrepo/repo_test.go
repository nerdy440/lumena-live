package pgrepo_test

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/auth/authsvc"
	"github.com/lumena/auth/pgrepo"
	"github.com/lumena/db"
)

// newTestRepo connects to a real local Postgres (set TEST_DATABASE_URL),
// runs every migration fresh, and returns a repo backed by it — skips
// entirely if no test database is configured, so `go test ./...` without
// Postgres running still passes (matches the "local dev needs nothing"
// design goal; this test only runs when someone deliberately points it
// at a real database).
func newTestRepo(t *testing.T) (*pgrepo.Repo, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	// Wipe to a known state so tests are independent of prior runs.
	_, err = pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	if err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.New(pool), pool
}

func TestCreateAccountAndFindByEmail(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	email := "alice@example.com"

	created, err := repo.CreateAccount(ctx, nil, &email, "PK")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected a generated account ID")
	}
	if !created.IsNewAccount {
		t.Fatal("expected IsNewAccount true on creation")
	}

	found, err := repo.FindByEmail(ctx, email)
	if err != nil {
		t.Fatalf("FindByEmail: %v", err)
	}
	if found == nil || found.ID != created.ID {
		t.Fatalf("FindByEmail returned %+v, want account %s", found, created.ID)
	}
	if found.Status != authsvc.StatusActive {
		t.Fatalf("status = %s, want active", found.Status)
	}
}

func TestFindByEmail_NotFoundReturnsNilNotError(t *testing.T) {
	repo, _ := newTestRepo(t)
	acc, err := repo.FindByEmail(context.Background(), "nobody@example.com")
	if err != nil {
		t.Fatalf("expected no error for a missing account, got %v", err)
	}
	if acc != nil {
		t.Fatalf("expected nil for a missing account, got %+v", acc)
	}
}

func TestPasswordHash_RoundTrips(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	email := "bob@example.com"
	acc, err := repo.CreateAccount(ctx, nil, &email, "US")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := repo.SetPasswordHash(ctx, acc.ID, "hash-value-123"); err != nil {
		t.Fatalf("SetPasswordHash: %v", err)
	}
	got, err := repo.GetPasswordHash(ctx, acc.ID)
	if err != nil {
		t.Fatalf("GetPasswordHash: %v", err)
	}
	if got != "hash-value-123" {
		t.Fatalf("hash = %q, want %q", got, "hash-value-123")
	}
}

func TestSessionLifecycle(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	email := "carol@example.com"
	acc, _ := repo.CreateAccount(ctx, nil, &email, "GB")

	sessID, err := repo.CreateSession(ctx, acc.ID, "device-1", "iPhone", "tokenhash-abc", "1.2.3.4")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sess, err := repo.GetSession(ctx, "tokenhash-abc")
	if err != nil || sess == nil {
		t.Fatalf("GetSession: %v, %+v", err, sess)
	}
	if sess.ID != sessID || sess.AccountID != acc.ID {
		t.Fatalf("unexpected session: %+v", sess)
	}
	if sess.RevokedAt != nil {
		t.Fatal("new session should not be revoked")
	}

	if err := repo.RotateSession(ctx, sessID, "tokenhash-xyz"); err != nil {
		t.Fatalf("RotateSession: %v", err)
	}
	// Old hash should no longer resolve; new one should.
	if s, _ := repo.GetSession(ctx, "tokenhash-abc"); s != nil {
		t.Fatal("old token hash should no longer resolve a session after rotation")
	}
	if s, err := repo.GetSession(ctx, "tokenhash-xyz"); err != nil || s == nil {
		t.Fatalf("expected the rotated token hash to resolve, got %v, %+v", err, s)
	}

	if err := repo.RevokeSession(ctx, sessID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	revoked, _ := repo.GetSession(ctx, "tokenhash-xyz")
	if revoked == nil || revoked.RevokedAt == nil {
		t.Fatal("expected RevokedAt to be set after RevokeSession")
	}
}

// TestSessionLifecycle_RawBinaryTokenHash guards against a real bug this
// integration test caught: authsvc computes token hashes as raw SHA-256/
// HMAC bytes reinterpreted as a Go string (string([]byte)) — a valid Go
// string, but essentially never valid UTF-8, which a Postgres TEXT column
// rejects outright ("invalid byte sequence for encoding UTF8"). The
// literal ASCII test strings above ("tokenhash-abc") never exercised this
// because they happen to already be valid UTF-8.
func TestSessionLifecycle_RawBinaryTokenHash(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	email := "dave@example.com"
	acc, _ := repo.CreateAccount(ctx, nil, &email, "GB")

	raw := sha256.Sum256([]byte("some-refresh-token"))
	tokenHash := string(raw[:]) // exactly how authsvc derives it — see token.RefreshToken.Hash

	sessID, err := repo.CreateSession(ctx, acc.ID, "device-1", "iPhone", tokenHash, "1.2.3.4")
	if err != nil {
		t.Fatalf("CreateSession with raw binary hash: %v", err)
	}
	sess, err := repo.GetSession(ctx, tokenHash)
	if err != nil || sess == nil {
		t.Fatalf("GetSession with raw binary hash: err=%v sess=%+v", err, sess)
	}
	if sess.ID != sessID || sess.TokenHash != tokenHash {
		t.Fatalf("token hash did not round-trip byte-for-byte: got %q want %q", sess.TokenHash, tokenHash)
	}
}

func TestAgeAssuranceAndDeletion(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	email := "dave@example.com"
	acc, _ := repo.CreateAccount(ctx, nil, &email, "IN")

	status, dob, err := repo.GetAgeStatus(ctx, acc.ID)
	if err != nil || status != authsvc.AgeUndeclared || dob != nil {
		t.Fatalf("expected fresh account to be undeclared with no DOB, got %v %v %v", status, dob, err)
	}

	birthDate := time.Date(1995, 6, 15, 0, 0, 0, 0, time.UTC)
	if err := repo.DeclareAge(ctx, acc.ID, birthDate); err != nil {
		t.Fatalf("DeclareAge: %v", err)
	}
	status, dob, err = repo.GetAgeStatus(ctx, acc.ID)
	if err != nil || status != authsvc.AgeDeclared || dob == nil || !dob.Equal(birthDate) {
		t.Fatalf("after declare: status=%v dob=%v err=%v", status, dob, err)
	}

	if err := repo.SetAgeStatus(ctx, acc.ID, authsvc.AgeAssured); err != nil {
		t.Fatalf("SetAgeStatus: %v", err)
	}
	status, _, _ = repo.GetAgeStatus(ctx, acc.ID)
	if status != authsvc.AgeAssured {
		t.Fatalf("status = %s, want assured", status)
	}

	// Deletion + cancel-deletion round trip.
	if err := repo.MarkAccountDeleted(ctx, acc.ID); err != nil {
		t.Fatalf("MarkAccountDeleted: %v", err)
	}
	found, _ := repo.FindByID(ctx, acc.ID)
	if found.Status != authsvc.StatusDeleted {
		t.Fatalf("status = %s, want deleted", found.Status)
	}
	if err := repo.CancelDeletion(ctx, acc.ID); err != nil {
		t.Fatalf("CancelDeletion: %v", err)
	}
	found, _ = repo.FindByID(ctx, acc.ID)
	if found.Status != authsvc.StatusActive {
		t.Fatalf("status = %s, want active after cancel", found.Status)
	}
}
