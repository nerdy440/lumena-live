package pgrepo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lumena/db"
	"github.com/lumena/referral"
	"github.com/lumena/referral/pgrepo"
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

func TestTryClaim_OnlyOncePerAccount(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	ok1, err := repo.TryClaim(ctx, "referred-1")
	if err != nil || !ok1 {
		t.Fatalf("first claim should succeed: ok=%v err=%v", ok1, err)
	}
	ok2, err := repo.TryClaim(ctx, "referred-1")
	if err != nil || ok2 {
		t.Fatalf("second claim by same account should fail: ok=%v err=%v", ok2, err)
	}
}

func TestRecordReward_AssignsIDAndCreatedAt(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	r := &referral.Referral{
		ReferrerID:    "alice",
		ReferredID:    "bob",
		RewardCoins:   referral.RewardCoins,
		TransactionID: "tx-1",
		CreatedAt:     time.Now(),
	}
	if err := repo.RecordReward(ctx, r); err != nil {
		t.Fatalf("record reward: %v", err)
	}
	if r.ID == "" {
		t.Fatal("expected RecordReward to assign an ID to the caller's struct")
	}

	list, err := repo.ListByReferrer(ctx, "alice")
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("list by referrer: %+v err=%v", list, err)
	}
}

func TestListByReferrer_MostRecentFirst(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	r1 := &referral.Referral{ReferrerID: "carol", ReferredID: "dave", RewardCoins: 100, TransactionID: "tx-a", CreatedAt: time.Now()}
	if err := repo.RecordReward(ctx, r1); err != nil {
		t.Fatalf("record reward 1: %v", err)
	}
	r2 := &referral.Referral{ReferrerID: "carol", ReferredID: "erin", RewardCoins: 100, TransactionID: "tx-b", CreatedAt: time.Now().Add(time.Second)}
	if err := repo.RecordReward(ctx, r2); err != nil {
		t.Fatalf("record reward 2: %v", err)
	}

	list, err := repo.ListByReferrer(ctx, "carol")
	if err != nil || len(list) != 2 {
		t.Fatalf("list by referrer: %+v err=%v", list, err)
	}
	if list[0].ID != r2.ID {
		t.Fatalf("expected most recent referral first, got %+v", list)
	}
}

func TestListByReferrer_EmptyForUnknownReferrer(t *testing.T) {
	repo := newTestRepo(t)
	list, err := repo.ListByReferrer(context.Background(), "nobody")
	if err != nil || len(list) != 0 {
		t.Fatalf("expected empty list, got %+v err=%v", list, err)
	}
}
