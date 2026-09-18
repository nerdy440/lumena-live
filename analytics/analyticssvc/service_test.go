package analyticssvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lumena/analytics"
	"github.com/lumena/analytics/analyticssvc"
)

func alwaysAllowed(context.Context, string) (bool, error) { return true, nil }
func neverAllowed(context.Context, string) (bool, error)  { return false, nil }

func noGifts(context.Context, time.Time) ([]analyticssvc.GiftTxn, error)       { return nil, nil }
func noPurchases(context.Context, time.Time) ([]analyticssvc.PurchaseTxn, error) { return nil, nil }
func noBroadcasts(context.Context) ([]analyticssvc.BroadcastSession, error)    { return nil, nil }

func TestGetDAUMAU_Forbidden(t *testing.T) {
	svc := analyticssvc.New(analytics.NewMemEventRepo(), neverAllowed, noGifts, noPurchases, noBroadcasts)
	_, err := svc.GetDAUMAU(context.Background(), "rando")
	if !errors.Is(err, analytics.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestGetDAUMAU_CountsDistinctAccountsInWindow(t *testing.T) {
	ctx := context.Background()
	repo := analytics.NewMemEventRepo()
	svc := analyticssvc.New(repo, alwaysAllowed, noGifts, noPurchases, noBroadcasts)

	svc.RecordEvent(ctx, "alice", analytics.EventLogin)
	svc.RecordEvent(ctx, "bob", analytics.EventLogin)
	svc.RecordEvent(ctx, "alice", analytics.EventLogin) // same day, same account — still 1

	d, err := svc.GetDAUMAU(ctx, "admin1")
	if err != nil {
		t.Fatal(err)
	}
	if d.DAU != 2 {
		t.Fatalf("expected DAU 2 (alice, bob), got %d", d.DAU)
	}
	if d.MAU != 2 {
		t.Fatalf("expected MAU 2, got %d", d.MAU)
	}
}

func TestGetFunnel_StepsAreMonotonicallyDecreasing(t *testing.T) {
	ctx := context.Background()
	repo := analytics.NewMemEventRepo()
	gifts := []analyticssvc.GiftTxn{
		{SenderID: "alice", RecipientID: "carol", CreatorDiamonds: 100, GrossCoins: 200, CreatedAt: time.Now()},
	}
	broadcasts := []analyticssvc.BroadcastSession{}
	svc := analyticssvc.New(repo, alwaysAllowed,
		func(context.Context, time.Time) ([]analyticssvc.GiftTxn, error) { return gifts, nil },
		noPurchases,
		func(context.Context) ([]analyticssvc.BroadcastSession, error) { return broadcasts, nil },
	)

	svc.RecordEvent(ctx, "alice", analytics.EventAccountCreated)
	svc.RecordEvent(ctx, "bob", analytics.EventAccountCreated)
	svc.RecordEvent(ctx, "carol", analytics.EventAccountCreated)
	svc.RecordEvent(ctx, "bob", analytics.EventLogin)

	steps, err := svc.GetFunnel(ctx, "admin1")
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 4 {
		t.Fatalf("expected 4 funnel steps, got %d", len(steps))
	}
	if steps[0].Name != "registered" || steps[0].Count != 3 {
		t.Fatalf("expected registered=3, got %+v", steps[0])
	}
	if steps[1].Name != "logged_in" || steps[1].Count != 3 {
		// all 3 registered accounts count as logged_in (registration issues a session) even without an explicit login event
		t.Fatalf("expected logged_in=3, got %+v", steps[1])
	}
	if steps[2].Name != "first_gift_sent" || steps[2].Count != 1 {
		t.Fatalf("expected first_gift_sent=1 (alice), got %+v", steps[2])
	}
	if steps[3].Name != "first_broadcast_started" || steps[3].Count != 0 {
		t.Fatalf("expected first_broadcast_started=0, got %+v", steps[3])
	}
	for i := 1; i < len(steps); i++ {
		if steps[i].Count > steps[i-1].Count {
			t.Fatalf("funnel step %d (%s=%d) exceeds step %d (%s=%d)", i, steps[i].Name, steps[i].Count, i-1, steps[i-1].Name, steps[i-1].Count)
		}
	}
}

func TestGetRetention_ComputesDay1Retention(t *testing.T) {
	ctx := context.Background()
	repo := analytics.NewMemEventRepo()
	svc := analyticssvc.New(repo, alwaysAllowed, noGifts, noPurchases, noBroadcasts)

	// Simulate: alice and bob registered "today"; only alice returns "tomorrow".
	// Since RecordEvent stamps CreatedAt = now(), we treat "today" as now and
	// verify the cohort/retention window logic directly against real time.
	svc.RecordEvent(ctx, "alice", analytics.EventAccountCreated)
	svc.RecordEvent(ctx, "bob", analytics.EventAccountCreated)

	points, err := svc.GetRetention(ctx, "admin1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 {
		t.Fatalf("expected 3 retention points (1/7/30 day), got %d", len(points))
	}
	if points[0].CohortSize != 2 {
		t.Fatalf("expected cohort size 2, got %d", points[0].CohortSize)
	}
	// Neither has returned "tomorrow" yet in this synchronous test.
	if points[0].RetainedCount != 0 {
		t.Fatalf("expected 0 retained at day 1 (no future events exist yet), got %d", points[0].RetainedCount)
	}
}

func TestGetEconomySummary_SumsGiftsAndPurchases(t *testing.T) {
	ctx := context.Background()
	repo := analytics.NewMemEventRepo()
	gifts := []analyticssvc.GiftTxn{
		{SenderID: "a", RecipientID: "b", GrossCoins: 100, CreatorDiamonds: 70, CreatedAt: time.Now()},
		{SenderID: "c", RecipientID: "b", GrossCoins: 50, CreatorDiamonds: 35, CreatedAt: time.Now()},
	}
	purchases := []analyticssvc.PurchaseTxn{
		{AccountID: "a", Coins: 1000, CreatedAt: time.Now()},
	}
	svc := analyticssvc.New(repo, alwaysAllowed,
		func(context.Context, time.Time) ([]analyticssvc.GiftTxn, error) { return gifts, nil },
		func(context.Context, time.Time) ([]analyticssvc.PurchaseTxn, error) { return purchases, nil },
		noBroadcasts,
	)

	summary, err := svc.GetEconomySummary(ctx, "admin1", 30)
	if err != nil {
		t.Fatal(err)
	}
	if summary.TotalGiftsSent != 2 {
		t.Fatalf("expected 2 gifts, got %d", summary.TotalGiftsSent)
	}
	if summary.TotalCoinsSpent != 150 {
		t.Fatalf("expected 150 coins spent, got %d", summary.TotalCoinsSpent)
	}
	if summary.TotalDiamondsIssued != 105 {
		t.Fatalf("expected 105 diamonds issued, got %d", summary.TotalDiamondsIssued)
	}
	if summary.TotalCoinsPurchased != 1000 {
		t.Fatalf("expected 1000 coins purchased, got %d", summary.TotalCoinsPurchased)
	}
}

func TestGetCreatorTrend_SelfViewNeedsNoPermission(t *testing.T) {
	ctx := context.Background()
	repo := analytics.NewMemEventRepo()
	gifts := []analyticssvc.GiftTxn{
		{SenderID: "fan1", RecipientID: "creator1", GrossCoins: 100, CreatorDiamonds: 70, CreatedAt: time.Now()},
	}
	svc := analyticssvc.New(repo, neverAllowed, // permission would deny — must not be consulted for self-view
		func(context.Context, time.Time) ([]analyticssvc.GiftTxn, error) { return gifts, nil },
		noPurchases, noBroadcasts,
	)
	points, err := svc.GetCreatorTrend(ctx, "creator1", "creator1", 30)
	if err != nil {
		t.Fatalf("expected creator to view their own trend without admin permission, got %v", err)
	}
	if len(points) != 1 || points[0].GiftsReceived != 1 {
		t.Fatalf("expected 1 bucketed day with 1 gift, got %+v", points)
	}
}

func TestGetCreatorTrend_ViewingOthersRequiresPermission(t *testing.T) {
	ctx := context.Background()
	repo := analytics.NewMemEventRepo()
	svc := analyticssvc.New(repo, neverAllowed, noGifts, noPurchases, noBroadcasts)
	_, err := svc.GetCreatorTrend(ctx, "finance1", "creator1", 30)
	if !errors.Is(err, analytics.ErrForbidden) {
		t.Fatalf("expected ErrForbidden viewing someone else's trend without permission, got %v", err)
	}
}
