package pgrepo_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/lumena/db"
	"github.com/lumena/rollout"
	"github.com/lumena/rollout/pgrepo"
)

func newTestRepos(t *testing.T) (*pgrepo.FlagRepo, *pgrepo.InternalRepo) {
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
	return pgrepo.NewFlagRepo(pool), pgrepo.NewInternalRepo(pool)
}

func TestFlag_GetNotFoundThenUpsertThenList(t *testing.T) {
	flags, _ := newTestRepos(t)
	ctx := context.Background()

	if _, err := flags.Get(ctx, "new_feature"); !errors.Is(err, rollout.ErrFlagNotFound) {
		t.Fatalf("expected ErrFlagNotFound, got %v", err)
	}

	f, err := flags.Upsert(ctx, "new_feature", rollout.StageInternal)
	if err != nil || f.Stage != rollout.StageInternal {
		t.Fatalf("upsert: %+v err=%v", f, err)
	}

	f2, err := flags.Upsert(ctx, "new_feature", rollout.Stage5Pct)
	if err != nil || f2.Stage != rollout.Stage5Pct || f2.CreatedAt != f.CreatedAt {
		t.Fatalf("re-upsert should update stage but keep created_at: %+v (orig created_at=%v)", f2, f.CreatedAt)
	}

	_, _ = flags.Upsert(ctx, "another_feature", rollout.StageOff)
	all, err := flags.List(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %+v err=%v", all, err)
	}
	if all[0].Key != "another_feature" || all[1].Key != "new_feature" {
		t.Fatalf("expected alphabetical order, got %+v", all)
	}
}

func TestInternalRepo_MarkThenIsInternal(t *testing.T) {
	_, internal := newTestRepos(t)
	ctx := context.Background()

	is, err := internal.IsInternal(ctx, "acc-1")
	if err != nil || is {
		t.Fatalf("should not be internal yet: %v err=%v", is, err)
	}
	if err := internal.MarkInternal(ctx, "acc-1"); err != nil {
		t.Fatalf("mark internal: %v", err)
	}
	is2, err := internal.IsInternal(ctx, "acc-1")
	if err != nil || !is2 {
		t.Fatalf("should now be internal: %v err=%v", is2, err)
	}
	// Idempotent.
	if err := internal.MarkInternal(ctx, "acc-1"); err != nil {
		t.Fatalf("re-mark internal: %v", err)
	}
}
