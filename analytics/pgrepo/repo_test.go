package pgrepo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lumena/analytics"
	"github.com/lumena/analytics/pgrepo"
	"github.com/lumena/db"
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

func TestAppend_ThenListSinceAndListByType(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	before := time.Now().Add(-time.Second)

	e1, err := r.Append(ctx, "acc-1", analytics.EventAccountCreated)
	if err != nil || e1.ID == "" || e1.AccountID != "acc-1" {
		t.Fatalf("append 1: %+v err=%v", e1, err)
	}
	e2, err := r.Append(ctx, "acc-1", analytics.EventLogin)
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	_, _ = r.Append(ctx, "acc-2", analytics.EventAccountCreated)

	since, err := r.ListSince(ctx, before)
	if err != nil || len(since) != 3 {
		t.Fatalf("list since: %+v err=%v", since, err)
	}

	future, err := r.ListSince(ctx, time.Now().Add(time.Hour))
	if err != nil || len(future) != 0 {
		t.Fatalf("list since future: %+v err=%v", future, err)
	}

	logins, err := r.ListByType(ctx, analytics.EventLogin)
	if err != nil || len(logins) != 1 || logins[0].ID != e2.ID {
		t.Fatalf("list by type login: %+v err=%v", logins, err)
	}

	created, err := r.ListByType(ctx, analytics.EventAccountCreated)
	if err != nil || len(created) != 2 {
		t.Fatalf("list by type account_created: %+v err=%v", created, err)
	}
}
