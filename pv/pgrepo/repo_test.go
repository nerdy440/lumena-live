package pgrepo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lumena/db"
	"github.com/lumena/pv"
	"github.com/lumena/pv/pgrepo"
)

func newTestRepo(t *testing.T) *pgrepo.Repo {
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

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.New(pool)
}

func TestCreateAndGetSession(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	s := &pv.Session{
		ID: "pv-1", CallerID: "alice", CalleeID: "bob", State: pv.StateRequested,
		RatePerMinCoins: 10, CreatedAt: time.Now().Truncate(time.Millisecond),
		ConsumedCoins: 10, HoldTransactionID: "tx-hold-1",
	}
	if err := r.Create(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := r.Get(ctx, "pv-1")
	if err != nil || got == nil {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if got.CallerID != "alice" || got.State != pv.StateRequested || got.ConsumedCoins != 10 || got.HoldTransactionID != "tx-hold-1" {
		t.Fatalf("unexpected session: %+v", got)
	}
}

func TestGet_NotFoundReturnsNilNotError(t *testing.T) {
	r := newTestRepo(t)
	got, err := r.Get(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil session, got %+v", got)
	}
}

func TestUpdate_AcceptThenEndLifecycle(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	s := &pv.Session{ID: "pv-2", CallerID: "alice", CalleeID: "bob", State: pv.StateRequested, RatePerMinCoins: 10, CreatedAt: time.Now()}
	if err := r.Create(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	now := time.Now().Truncate(time.Millisecond)
	s.State = pv.StateAccepted
	s.AcceptedAt = &now
	if err := r.Update(ctx, s); err != nil {
		t.Fatalf("update accept: %v", err)
	}
	got, _ := r.Get(ctx, "pv-2")
	if got.State != pv.StateAccepted || got.AcceptedAt == nil {
		t.Fatalf("accept not persisted: %+v", got)
	}

	ended := now.Add(90 * time.Second).Truncate(time.Millisecond)
	s.State = pv.StateEnded
	s.EndedAt = &ended
	s.ConsumedCoins = 20
	if err := r.Update(ctx, s); err != nil {
		t.Fatalf("update end: %v", err)
	}
	got2, _ := r.Get(ctx, "pv-2")
	if got2.State != pv.StateEnded || got2.EndedAt == nil || got2.ConsumedCoins != 20 {
		t.Fatalf("end not persisted: %+v", got2)
	}
}

func TestUpdate_UnknownSessionReturnsErrSessionNotFound(t *testing.T) {
	r := newTestRepo(t)
	err := r.Update(context.Background(), &pv.Session{ID: "does-not-exist", State: pv.StateEnded})
	if err != pv.ErrSessionNotFound {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestListByAccount_FindsBothCallerAndCalleeRoles(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	_ = r.Create(ctx, &pv.Session{ID: "pv-a", CallerID: "alice", CalleeID: "bob", State: pv.StateEnded, RatePerMinCoins: 10, CreatedAt: time.Now()})
	_ = r.Create(ctx, &pv.Session{ID: "pv-b", CallerID: "carol", CalleeID: "alice", State: pv.StateEnded, RatePerMinCoins: 10, CreatedAt: time.Now()})
	_ = r.Create(ctx, &pv.Session{ID: "pv-c", CallerID: "dave", CalleeID: "erin", State: pv.StateEnded, RatePerMinCoins: 10, CreatedAt: time.Now()})

	sessions, err := r.ListByAccount(ctx, "alice")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions for alice (as caller and callee), got %d: %+v", len(sessions), sessions)
	}
}
