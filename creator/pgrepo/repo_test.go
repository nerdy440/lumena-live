package pgrepo_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/creator"
	"github.com/lumena/creator/pgrepo"
	"github.com/lumena/db"
)

func newTestRepos(t *testing.T) (*pgrepo.KYCRepo, *pgrepo.PayoutRepo, *pgrepo.PayoutProfileRepo, *pgxpool.Pool) {
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
	return pgrepo.NewKYCRepo(pool), pgrepo.NewPayoutRepo(pool), pgrepo.NewPayoutProfileRepo(pool), pool
}

func TestKYC_DefaultsThenSubmitThenDecide(t *testing.T) {
	kyc, _, _, _ := newTestRepos(t)
	ctx := context.Background()

	def, err := kyc.Get(ctx, "acc-1")
	if err != nil || def.Status != creator.KYCNone {
		t.Fatalf("expected default KYCNone, got %+v err=%v", def, err)
	}

	submitted, err := kyc.Submit(ctx, "acc-1", "Alice Example", "PK")
	if err != nil || submitted.Status != creator.KYCPending || submitted.LegalName != "Alice Example" {
		t.Fatalf("submit: %+v err=%v", submitted, err)
	}

	decided, err := kyc.Decide(ctx, "acc-1", creator.KYCVerified)
	if err != nil || decided.Status != creator.KYCVerified || decided.DecidedAt == nil {
		t.Fatalf("decide: %+v err=%v", decided, err)
	}
	// Legal name/country must survive the decide-only update.
	if decided.LegalName != "Alice Example" {
		t.Fatalf("decide must not clobber submitted fields: %+v", decided)
	}
}

func TestPayout_CreateGetUpdateStatus(t *testing.T) {
	_, payouts, _, _ := newTestRepos(t)
	ctx := context.Background()

	p, err := payouts.Create(ctx, creator.Payout{AccountID: "acc-1", AmountDiamonds: 500, TransactionID: "tx-1"})
	if err != nil || p.Status != creator.PayoutRequested || p.ID == "" {
		t.Fatalf("create: %+v err=%v", p, err)
	}

	got, err := payouts.Get(ctx, p.ID)
	if err != nil || got.AmountDiamonds != 500 {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	if err := payouts.UpdateStatus(ctx, p.ID, creator.PayoutFailed, "bank rejected"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	got2, _ := payouts.Get(ctx, p.ID)
	if got2.Status != creator.PayoutFailed || got2.FailureReason != "bank rejected" {
		t.Fatalf("status update not persisted: %+v", got2)
	}

	if _, err := payouts.Get(ctx, "payout-9999"); !errors.Is(err, creator.ErrPayoutNotFound) {
		t.Fatalf("expected ErrPayoutNotFound, got %v", err)
	}
}

func TestPayout_UpdateAppliesMutateAtomically(t *testing.T) {
	_, payouts, _, _ := newTestRepos(t)
	ctx := context.Background()
	p, _ := payouts.Create(ctx, creator.Payout{AccountID: "acc-1", AmountDiamonds: 200})

	err := payouts.Update(ctx, p.ID, func(payout *creator.Payout) error {
		payout.Status = creator.PayoutApproved
		payout.ReviewedBy = "admin-1"
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := payouts.Get(ctx, p.ID)
	if got.Status != creator.PayoutApproved || got.ReviewedBy != "admin-1" {
		t.Fatalf("mutate not applied: %+v", got)
	}
}

func TestPayout_PendingOrProcessingTotalAndCountPending(t *testing.T) {
	_, payouts, _, _ := newTestRepos(t)
	ctx := context.Background()

	p1, _ := payouts.Create(ctx, creator.Payout{AccountID: "acc-1", AmountDiamonds: 100})
	p2, _ := payouts.Create(ctx, creator.Payout{AccountID: "acc-1", AmountDiamonds: 300})
	p3, _ := payouts.Create(ctx, creator.Payout{AccountID: "acc-1", AmountDiamonds: 999})
	_ = payouts.UpdateStatus(ctx, p2.ID, creator.PayoutProcessing, "")
	_ = payouts.UpdateStatus(ctx, p3.ID, creator.PayoutPaid, "") // no longer reserved
	_ = p1

	total, err := payouts.PendingOrProcessingTotal(ctx, "acc-1")
	if err != nil || total != 400 { // p1 (requested, 100) + p2 (processing, 300)
		t.Fatalf("pending total = %d, want 400 (err=%v)", total, err)
	}

	n, err := payouts.CountPending(ctx)
	if err != nil || n != 2 {
		t.Fatalf("count pending = %d, want 2 (err=%v)", n, err)
	}
}

func TestPayout_ListAllFilterAndListByAccountOrdering(t *testing.T) {
	_, payouts, _, _ := newTestRepos(t)
	ctx := context.Background()
	p1, _ := payouts.Create(ctx, creator.Payout{AccountID: "acc-1", AmountDiamonds: 100})
	p2, _ := payouts.Create(ctx, creator.Payout{AccountID: "acc-1", AmountDiamonds: 200})
	_ = payouts.UpdateStatus(ctx, p1.ID, creator.PayoutPaid, "")

	paidOnly, err := payouts.ListAll(ctx, creator.PayoutPaid)
	if err != nil || len(paidOnly) != 1 || paidOnly[0].ID != p1.ID {
		t.Fatalf("ListAll(paid): %+v err=%v", paidOnly, err)
	}

	all, err := payouts.ListAll(ctx, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAll(all): %+v err=%v", all, err)
	}

	byAccount, err := payouts.ListByAccount(ctx, "acc-1")
	if err != nil || len(byAccount) != 2 || byAccount[0].ID != p2.ID {
		t.Fatalf("expected newest-first (p2 then p1), got %+v err=%v", byAccount, err)
	}
}

func TestPayoutProfile_DefaultThenSet(t *testing.T) {
	_, _, profiles, _ := newTestRepos(t)
	ctx := context.Background()

	def, err := profiles.Get(ctx, "acc-1")
	if err != nil || def.Method != "" {
		t.Fatalf("expected zero-value profile, got %+v err=%v", def, err)
	}

	set, err := profiles.Set(ctx, creator.PayoutProfile{
		AccountID: "acc-1", Method: creator.PayoutMethodMobileWallet, Country: "PK",
		PayeeName: "Alice", Destination: "0300-1234567",
	})
	if err != nil || set.Method != creator.PayoutMethodMobileWallet || set.Destination != "0300-1234567" {
		t.Fatalf("set: %+v err=%v", set, err)
	}

	got, err := profiles.Get(ctx, "acc-1")
	if err != nil || got.Destination != "0300-1234567" {
		t.Fatalf("get after set: %+v err=%v", got, err)
	}
}
