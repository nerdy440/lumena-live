package giftsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lumena/ledger"
	"github.com/lumena/ledger/giftsvc"
)

func seed(t *testing.T, repo *ledger.MemLedger, accountID string, coins int64) {
	t.Helper()
	_, _, err := repo.PostTransaction(context.Background(), "promo", "seed-"+accountID, nil, []ledger.Entry{
		{AccountID: accountID, Amount: coins, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: -coins, Currency: ledger.Coin},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestSendGift_DebitsSenderCreditsCreatorDiamondsAndPlatformTake(t *testing.T) {
	repo := ledger.NewMemLedger()
	svc := giftsvc.NewService(repo)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	result, err := svc.SendGift(ctx, "alice", "bob", "room-1", "g_heart", 1, "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.CoinsSpent != 50 {
		t.Errorf("expected 50 coins spent (g_heart), got %d", result.CoinsSpent)
	}
	if result.PlatformTook != 15 { // 30% of 50
		t.Errorf("expected platform take 15, got %d", result.PlatformTook)
	}
	if result.CreatorDiamonds != 24 { // (50-15)=35 coins equiv * 700/1000 = 24 (integer division)
		t.Errorf("expected 24 diamonds credited, got %d", result.CreatorDiamonds)
	}
	if result.NewSenderBalance != 950 {
		t.Errorf("expected sender balance 950, got %d", result.NewSenderBalance)
	}

	senderCoins, _, _ := svc.Balance(ctx, "alice")
	if senderCoins != 950 {
		t.Errorf("expected alice's coin balance 950, got %d", senderCoins)
	}
	_, bobDiamonds, _ := svc.Balance(ctx, "bob")
	if bobDiamonds != 24 {
		t.Errorf("expected bob's diamond balance 24, got %d", bobDiamonds)
	}
}

func TestSendGift_IdempotentOnRetry_ChargesExactlyOnce(t *testing.T) {
	repo := ledger.NewMemLedger()
	svc := giftsvc.NewService(repo)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	r1, err := svc.SendGift(ctx, "alice", "bob", "room-1", "g_heart", 1, "same-key")
	if err != nil || !r1.IsNew {
		t.Fatalf("expected first send to be new, got isNew=%v err=%v", r1.IsNew, err)
	}
	r2, err := svc.SendGift(ctx, "alice", "bob", "room-1", "g_heart", 1, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if r2.IsNew {
		t.Fatal("expected retry with same idempotency key to not charge again")
	}
	if r1.TransactionID != r2.TransactionID {
		t.Fatalf("expected same transaction id on replay, got %q vs %q", r1.TransactionID, r2.TransactionID)
	}

	bal, _, _ := svc.Balance(ctx, "alice")
	if bal != 950 {
		t.Fatalf("expected balance charged exactly once (950), got %d — indicates double-spend", bal)
	}
}

func TestSendGift_InsufficientBalance_LeavesBalanceUnchangedWithShortfall(t *testing.T) {
	repo := ledger.NewMemLedger()
	svc := giftsvc.NewService(repo)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 10)

	_, err := svc.SendGift(ctx, "alice", "bob", "room-1", "g_heart", 1, "idem-1") // costs 50, has 10
	var insufficient *ledger.InsufficientBalanceError
	if !errors.As(err, &insufficient) {
		t.Fatalf("expected InsufficientBalanceError, got %v", err)
	}
	if insufficient.Shortfall() != 40 {
		t.Fatalf("expected shortfall 40 (need 50, have 10), got %d", insufficient.Shortfall())
	}

	bal, _, _ := svc.Balance(ctx, "alice")
	if bal != 10 {
		t.Fatalf("expected balance unchanged at 10 after rejected gift, got %d", bal)
	}
}

func TestSendGift_UnknownGiftRejected(t *testing.T) {
	repo := ledger.NewMemLedger()
	svc := giftsvc.NewService(repo)
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	_, err := svc.SendGift(context.Background(), "alice", "bob", "room-1", "g_does_not_exist", 1, "idem-1")
	if !errors.Is(err, giftsvc.ErrGiftNotFound) {
		t.Fatalf("expected ErrGiftNotFound, got %v", err)
	}
}

func TestSendGift_SelfGiftRejected(t *testing.T) {
	repo := ledger.NewMemLedger()
	svc := giftsvc.NewService(repo)
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	_, err := svc.SendGift(context.Background(), "alice", "alice", "room-1", "g_heart", 1, "idem-1")
	if !errors.Is(err, giftsvc.ErrSelfGift) {
		t.Fatalf("expected ErrSelfGift, got %v", err)
	}
}

func TestSendGift_QuantityMultipliesCost(t *testing.T) {
	repo := ledger.NewMemLedger()
	svc := giftsvc.NewService(repo)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	result, err := svc.SendGift(ctx, "alice", "bob", "room-1", "g_rose", 5, "idem-1") // 10 coins * 5
	if err != nil {
		t.Fatal(err)
	}
	if result.CoinsSpent != 50 {
		t.Fatalf("expected 50 coins spent (10*5), got %d", result.CoinsSpent)
	}
}

// TestSendGift_RejectsQuantityLargeEnoughToOverflow is a regression test
// for a real, confirmed integer-overflow exploit: quantity was
// client-supplied int64 with only a "> 0" check, and gift.Coins * quantity
// had no overflow guard. 239807672958224171 is not an arbitrary huge
// number — it's the exact quantity of g_aurora (2000 coins) that makes
// 2000*quantity wrap to exactly -16 in int64 two's-complement arithmetic.
// Because ledger.PostTransaction only rejects an entry that would drive a
// balance NEGATIVE — never one that makes it suspiciously positive — a
// wrapped-negative grossCoins flips the sender's debit into a real CREDIT
// that sails through untouched. Run against the actual (unfixed) code,
// this exact quantity let an attacker with a room and a victim already
// holding >= 8 diamonds mint 16 coins from nothing while draining 8
// diamonds from the victim's balance without their consent — verified by
// temporarily removing the maxGiftQuantity bound and observing the sender
// gain coins and the recipient involuntarily lose diamonds. This asserts
// the fixed behavior: the send is rejected outright, before any of that
// arithmetic runs, and neither balance moves.
func TestSendGift_RejectsQuantityLargeEnoughToOverflow(t *testing.T) {
	repo := ledger.NewMemLedger()
	svc := giftsvc.NewService(repo)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)
	// would-be victim, seeded with diamonds directly (the shared `seed`
	// helper only credits COIN, never DIAMOND).
	if _, _, err := repo.PostTransaction(ctx, "promo", "seed-bob-diamonds", nil, []ledger.Entry{
		{AccountID: ledger.UserDiamondsAccount("bob"), Amount: 100, Currency: ledger.Diamond},
		{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -100, Currency: ledger.Diamond},
	}); err != nil {
		t.Fatal(err)
	}

	const exploitQuantity = 239807672958224171 // 2000 * this ≡ -16 (mod 2^64)

	_, err := svc.SendGift(ctx, "alice", "bob", "room-1", "g_aurora", exploitQuantity, "idem-1")
	if !errors.Is(err, giftsvc.ErrInvalidQuantity) {
		t.Fatalf("expected ErrInvalidQuantity for the overflow-exploit quantity, got %v", err)
	}

	aliceBal, _ := repo.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	bobBal, _ := repo.Balance(ctx, ledger.UserDiamondsAccount("bob"), ledger.Diamond)
	if aliceBal != 1000 {
		t.Fatalf("expected alice's coins unchanged at 1000, got %d — she must never mint coins via overflow", aliceBal)
	}
	if bobBal != 100 {
		t.Fatalf("expected bob's diamonds unchanged at 100, got %d — he must never be drained without consent via overflow", bobBal)
	}
}

// ─── Room/host verification (security fix) ─────────────────────────────────

func TestSendGift_RejectsRecipientNotRoomHost(t *testing.T) {
	repo := ledger.NewMemLedger()
	verifyHost := func(_ context.Context, roomID, recipientID string) (bool, error) {
		return roomID == "room-1" && recipientID == "bob", nil // only bob is room-1's host
	}
	svc := giftsvc.NewService(repo).WithRoomHostCheck(verifyHost)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	// mallory is not room-1's host — must be rejected even though the gift
	// itself (gift id, quantity, sender balance) is otherwise valid.
	_, err := svc.SendGift(ctx, "alice", "mallory", "room-1", "g_rose", 1, "idem-1")
	if !errors.Is(err, giftsvc.ErrRecipientNotRoomHost) {
		t.Fatalf("expected ErrRecipientNotRoomHost, got %v", err)
	}
	bal, _ := repo.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal != 1000 {
		t.Fatalf("expected balance unchanged at 1000 after a rejected gift, got %d", bal)
	}
}

func TestSendGift_EmptyRoomIDCannotBypassHostCheck(t *testing.T) {
	// Regression test for a real bypass: room_id == "" used to skip the
	// host-verification check entirely, letting a client route a gift's
	// payout to an arbitrary recipient_id with no room context at all.
	repo := ledger.NewMemLedger()
	verifyHostCalled := false
	verifyHost := func(_ context.Context, roomID, recipientID string) (bool, error) {
		verifyHostCalled = true
		return true, nil
	}
	svc := giftsvc.NewService(repo).WithRoomHostCheck(verifyHost)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	_, err := svc.SendGift(ctx, "alice", "anyone-at-all", "", "g_rose", 1, "idem-1")
	if !errors.Is(err, giftsvc.ErrRecipientNotRoomHost) {
		t.Fatalf("expected ErrRecipientNotRoomHost for an empty room_id, got %v", err)
	}
	if verifyHostCalled {
		t.Fatal("verifyHost should never even be called for an empty room_id — there's no room to check against")
	}
	bal, _ := repo.Balance(ctx, ledger.UserCoinsAccount("alice"), ledger.Coin)
	if bal != 1000 {
		t.Fatalf("expected balance unchanged at 1000 after a rejected gift, got %d", bal)
	}
}

func TestSendGift_AllowsRecipientWhoIsRoomHost(t *testing.T) {
	repo := ledger.NewMemLedger()
	verifyHost := func(_ context.Context, roomID, recipientID string) (bool, error) {
		return roomID == "room-1" && recipientID == "bob", nil
	}
	svc := giftsvc.NewService(repo).WithRoomHostCheck(verifyHost)
	ctx := context.Background()
	seed(t, repo, ledger.UserCoinsAccount("alice"), 1000)

	result, err := svc.SendGift(ctx, "alice", "bob", "room-1", "g_rose", 1, "idem-1")
	if err != nil {
		t.Fatalf("expected the real room host to receive the gift, got %v", err)
	}
	if result.CoinsSpent != 10 {
		t.Fatalf("expected 10 coins spent, got %d", result.CoinsSpent)
	}
}
