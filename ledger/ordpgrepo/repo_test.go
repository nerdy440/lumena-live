package ordpgrepo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lumena/db"
	"github.com/lumena/ledger"
	"github.com/lumena/ledger/ordpgrepo"
)

func newTestRepos(t *testing.T) (*ordpgrepo.OrderRepo, *ordpgrepo.SpendLimitsRepo) {
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
	return ordpgrepo.NewOrderRepo(pool), ordpgrepo.NewSpendLimitsRepo(pool)
}

func TestOrder_CreateGetAndUnknownSKU(t *testing.T) {
	orders, _ := newTestRepos(t)
	ctx := context.Background()

	o, err := orders.CreateOrder(ctx, "acc-1", "coins_500")
	if err != nil || o.ID == "" || o.Coins != 500 || o.Status != ledger.OrderCreated {
		t.Fatalf("create order: %+v err=%v", o, err)
	}

	got, err := orders.GetOrder(ctx, o.ID)
	if err != nil || got.AccountID != "acc-1" {
		t.Fatalf("get order: %+v err=%v", got, err)
	}

	if _, err := orders.CreateOrder(ctx, "acc-1", "not-a-real-sku"); err != ledger.ErrUnknownSKU {
		t.Fatalf("expected ErrUnknownSKU, got %v", err)
	}

	if _, err := orders.GetOrder(ctx, "nonexistent"); err != ledger.ErrOrderNotFound {
		t.Fatalf("expected ErrOrderNotFound, got %v", err)
	}
}

func TestOrder_UpdateOrderAndFindByTokenHash(t *testing.T) {
	orders, _ := newTestRepos(t)
	ctx := context.Background()
	o, _ := orders.CreateOrder(ctx, "acc-2", "coins_100")

	noToken, err := orders.FindByTokenHash(ctx, "hash-abc")
	if err != nil || noToken != nil {
		t.Fatalf("expected nil for unused token hash, got %+v err=%v", noToken, err)
	}

	o.TokenHash = "hash-abc"
	o.Status = ledger.OrderPaid
	o.PurchaseToken = "raw-token"
	if err := orders.UpdateOrder(ctx, o); err != nil {
		t.Fatalf("update order: %v", err)
	}

	found, err := orders.FindByTokenHash(ctx, "hash-abc")
	if err != nil || found == nil || found.ID != o.ID || found.Status != ledger.OrderPaid {
		t.Fatalf("find by token hash: %+v err=%v", found, err)
	}

	if err := orders.UpdateOrder(ctx, &ledger.Order{ID: "nonexistent"}); err != ledger.ErrOrderNotFound {
		t.Fatalf("expected ErrOrderNotFound updating a missing order, got %v", err)
	}
}

func TestOrder_ListOrdersAndListNonTerminal(t *testing.T) {
	orders, _ := newTestRepos(t)
	ctx := context.Background()
	o1, _ := orders.CreateOrder(ctx, "acc-3", "coins_100")
	o2, _ := orders.CreateOrder(ctx, "acc-3", "coins_500")
	_, _ = orders.CreateOrder(ctx, "acc-4", "coins_100")

	o1.Status = ledger.OrderPendingPayment
	_ = orders.UpdateOrder(ctx, o1)
	o2.Status = ledger.OrderCredited
	_ = orders.UpdateOrder(ctx, o2)

	all, err := orders.ListOrders(ctx, "acc-3", "")
	if err != nil || len(all) != 2 {
		t.Fatalf("list all orders for acc-3: %+v err=%v", all, err)
	}

	pending, err := orders.ListOrders(ctx, "acc-3", ledger.OrderPendingPayment)
	if err != nil || len(pending) != 1 || pending[0].ID != o1.ID {
		t.Fatalf("list pending orders: %+v err=%v", pending, err)
	}

	nonTerminal, err := orders.ListNonTerminal(ctx)
	if err != nil || len(nonTerminal) != 1 || nonTerminal[0].ID != o1.ID {
		t.Fatalf("list non-terminal: %+v err=%v", nonTerminal, err)
	}
}

func TestSpendLimits_GetDefaultsThenSetRoundTrips(t *testing.T) {
	_, limits := newTestRepos(t)
	ctx := context.Background()

	def, err := limits.Get(ctx, "acc-5")
	if err != nil || def.AccountID != "acc-5" || def.DailyCap != 0 {
		t.Fatalf("expected zero-value defaults for unknown account, got %+v err=%v", def, err)
	}

	until := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	l := &ledger.SpendLimits{
		AccountID:          "acc-5",
		DailyCap:           1000,
		WeeklyCap:          5000,
		MonthlyCap:         20000,
		CoolingOff:         true,
		CoolingOffUntil:    until,
		PendingDailyCap:    2000,
		PendingEffectiveAt: until,
	}
	if err := limits.Set(ctx, l); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := limits.Get(ctx, "acc-5")
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if got.DailyCap != 1000 || got.WeeklyCap != 5000 || got.MonthlyCap != 20000 || !got.CoolingOff {
		t.Fatalf("caps/cooling-off not persisted: %+v", got)
	}
	if !got.CoolingOffUntil.Equal(until) || !got.PendingEffectiveAt.Equal(until) {
		t.Fatalf("timestamps not persisted correctly: %+v", got)
	}
	if got.PendingDailyCap != 2000 {
		t.Fatalf("pending daily cap not persisted: %+v", got)
	}

	// Overwrite via upsert.
	l.DailyCap = 500
	l.CoolingOff = false
	if err := limits.Set(ctx, l); err != nil {
		t.Fatalf("set (update): %v", err)
	}
	got2, _ := limits.Get(ctx, "acc-5")
	if got2.DailyCap != 500 || got2.CoolingOff {
		t.Fatalf("upsert did not overwrite: %+v", got2)
	}
}
