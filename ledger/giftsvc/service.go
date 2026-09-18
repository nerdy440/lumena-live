// Package giftsvc implements gift sending (doc 12 Phase 8, doc 11 §3-4):
// a fixed catalogue, idempotent send with a 30% platform take, and the
// COIN→DIAMOND exchange rate posted through ledger.Repo as one balanced
// double-entry transaction. No optimistic balance update happens here —
// callers always render from this service's response (doc 11 §4, step 7).
package giftsvc

import (
	"context"
	"errors"
	"time"

	"github.com/lumena/ledger"
)

// Gift is a catalogue entry. AssetURL is a placeholder — the real Lottie/
// Rive asset pipeline (doc 12 Phase 8: "streamed not bundled") is out of
// scope for this dev build, same as streaming's DevPackagerService stands
// in for the real LL-HLS pipeline.
type Gift struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Coins    int64  `json:"coins"`
	AssetURL string `json:"asset_url"`
}

// Catalogue is the fixed dev gift list (doc 12 Phase 8: "Gift catalogue").
var Catalogue = []Gift{
	{ID: "g_rose", Name: "Rose", Coins: 10, AssetURL: "/assets/gifts/rose.json"},
	{ID: "g_heart", Name: "Heart", Coins: 50, AssetURL: "/assets/gifts/heart.json"},
	{ID: "g_crown", Name: "Crown", Coins: 500, AssetURL: "/assets/gifts/crown.json"},
	{ID: "g_aurora", Name: "Aurora", Coins: 2000, AssetURL: "/assets/gifts/aurora.json"},
}

func giftByID(id string) (Gift, bool) {
	for _, g := range Catalogue {
		if g.ID == id {
			return g, true
		}
	}
	return Gift{}, false
}

var (
	ErrGiftNotFound  = errors.New("giftsvc: unknown gift id")
	ErrSelfGift       = errors.New("giftsvc: cannot gift yourself")
	ErrInvalidQuantity = errors.New("giftsvc: quantity must be positive and no more than the maximum allowed per send")
	// ErrRecipientNotRoomHost is returned when a gift names a room_id but
	// recipient_id isn't that room's actual host — without this check a
	// client could send a room_id/recipient_id pair that don't correspond,
	// letting an attacker route a gift (and its creator_diamonds payout) to
	// an arbitrary account while the room's real host and its viewers see
	// nothing. Caught here, server-side, rather than trusted from the client.
	ErrRecipientNotRoomHost = errors.New("giftsvc: recipient is not this room's host")
	// ErrVelocityLimitExceeded is returned by the fraud module's PreSendFunc
	// gate (Phase 18, AF-04) — defined here rather than in fraud so the
	// existing gift-send error handling path (writeGiftError) has a
	// stable sentinel to match on without importing fraud.
	ErrVelocityLimitExceeded = errors.New("giftsvc: too many gifts sent recently — try again in a moment")
)

// Economy parameters — doc 11 §3: "declared in config, not hardcoded".
// Fixed constants here for this dev build; a real deployment loads these
// from config so they can change without a redeploy.
const (
	platformTakeBPS          = 3000 // 30%, doc 11 §3's worked example
	diamondsPerThousandCoins = 700  // 0.7 diamonds per coin, matching the abandoned backend/pkg/money reference constant

	// maxGiftQuantity bounds the client-supplied quantity in gift.Coins *
	// quantity. Without an upper bound, a large enough quantity overflows
	// int64 and wraps — since the most expensive gift (g_aurora, 2000
	// coins) times a suitably chosen quantity can be made to wrap to any
	// value including negative, that would flip the sender's coin DEBIT
	// into a CREDIT (ledger.PostTransaction only rejects entries that make
	// a balance negative, never one that makes it suspiciously positive),
	// letting an attacker mint coins from nothing. No real product lets a
	// user send billions of gifts in one call anyway, so this is both the
	// safety fix and a sane product limit.
	maxGiftQuantity = 10_000
)

// PreSendFunc is a pre-authorization gate consulted before a gift is
// posted — the fraud module's velocity limiter (Phase 18, AF-04) wires
// this to reject a send that would exceed a sender's gift rate limit.
// Optional; nil means no velocity gate.
type PreSendFunc func(ctx context.Context, senderID string) error

// SentHook is notified after a gift successfully posts — the fraud
// module's gift-loop detector (Phase 18, AF-03) wires this to check for
// A→B/B→A circular flows in real time. Optional.
type SentHook func(ctx context.Context, senderID, recipientID string, at time.Time)

// VerifyRoomHostFunc reports whether recipientID is roomID's actual host —
// the composition root wires this to the feed module's room repo. Consulted
// whenever roomID is non-empty so a gift's payout can never be routed to an
// account that doesn't match the room the client claims to be gifting in.
// Optional; nil skips the check (kept optional so existing tests that don't
// model rooms at all keep working unchanged).
type VerifyRoomHostFunc func(ctx context.Context, roomID, recipientID string) (bool, error)

type Service struct {
	repo         ledger.Repo
	preSend      PreSendFunc
	onSent       SentHook
	verifyHost   VerifyRoomHostFunc
}

func NewService(repo ledger.Repo) *Service {
	return &Service{repo: repo}
}

// WithVelocityCheck registers the fraud module's rate limiter.
func (s *Service) WithVelocityCheck(check PreSendFunc) *Service {
	s.preSend = check
	return s
}

// WithSentHook registers the fraud module's gift-loop detector.
func (s *Service) WithSentHook(hook SentHook) *Service {
	s.onSent = hook
	return s
}

// WithRoomHostCheck registers the room/host verification gate.
func (s *Service) WithRoomHostCheck(check VerifyRoomHostFunc) *Service {
	s.verifyHost = check
	return s
}

func (s *Service) Catalogue() []Gift { return Catalogue }

// Balance returns senderID's spendable coin balance and earned diamonds.
func (s *Service) Balance(ctx context.Context, accountID string) (coins, diamonds int64, err error) {
	coins, err = s.repo.Balance(ctx, ledger.UserCoinsAccount(accountID), ledger.Coin)
	if err != nil {
		return 0, 0, err
	}
	diamonds, err = s.repo.Balance(ctx, ledger.UserDiamondsAccount(accountID), ledger.Diamond)
	return coins, diamonds, err
}

// GiftResult is what a successful (or idempotent-replay) send returns.
type GiftResult struct {
	TransactionID   string
	IsNew           bool
	GiftID          string
	Quantity        int64
	CoinsSpent      int64
	CreatorDiamonds int64
	PlatformTook    int64
	NewSenderBalance int64
}

// SendGift debits senderID grossCoins = gift.Coins*quantity, credits
// recipientID diamonds at the declared exchange rate, and takes the
// platform's cut — all as one balanced transaction, idempotent on
// idempotencyKey (doc 11 §4's "the gift is sent exactly once").
func (s *Service) SendGift(ctx context.Context, senderID, recipientID, roomID, giftID string, quantity int64, idempotencyKey string) (*GiftResult, error) {
	if senderID == recipientID {
		return nil, ErrSelfGift
	}
	if quantity <= 0 || quantity > maxGiftQuantity {
		return nil, ErrInvalidQuantity
	}
	gift, ok := giftByID(giftID)
	if !ok {
		return nil, ErrGiftNotFound
	}

	// Idempotency short-circuit before recomputing shares, so a retry never
	// even re-derives amounts, let alone re-charges.
	if existing, _ := s.repo.GetTransaction(ctx, idempotencyKey); existing != nil {
		return s.finalizeResult(ctx, existing, senderID, false)
	}

	if s.preSend != nil {
		if err := s.preSend(ctx, senderID); err != nil {
			return nil, err
		}
	}

	if s.verifyHost != nil {
		// An empty room_id must NOT skip this check — that would let a
		// client route a gift's payout to an arbitrary recipient_id with no
		// room context at all, bypassing the very verification this gate
		// exists to enforce. A lookup error (e.g. unknown room_id) is
		// treated the same as "not the host" — from the client's
		// perspective an invalid/missing room_id is a bad request, not a
		// server fault, and either way the gift must not proceed.
		if roomID == "" {
			return nil, ErrRecipientNotRoomHost
		}
		ok, err := s.verifyHost(ctx, roomID, recipientID)
		if err != nil || !ok {
			return nil, ErrRecipientNotRoomHost
		}
	}

	grossCoins := gift.Coins * quantity
	platformShare := grossCoins * platformTakeBPS / 10000
	creatorCoinsEquivalent := grossCoins - platformShare
	creatorDiamonds := creatorCoinsEquivalent * diamondsPerThousandCoins / 1000

	entries := []ledger.Entry{
		{AccountID: ledger.UserCoinsAccount(senderID), Amount: -grossCoins, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: platformShare, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: creatorCoinsEquivalent, Currency: ledger.Coin},
		{AccountID: ledger.UserDiamondsAccount(recipientID), Amount: creatorDiamonds, Currency: ledger.Diamond},
		{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -creatorDiamonds, Currency: ledger.Diamond},
	}
	metadata := map[string]any{
		"gift_id": giftID, "room_id": roomID, "quantity": quantity,
		"sender_id": senderID, "recipient_id": recipientID,
	}

	tx, isNew, err := s.repo.PostTransaction(ctx, "gift", idempotencyKey, metadata, entries)
	if err != nil {
		return nil, err
	}
	if isNew && s.onSent != nil {
		s.onSent(ctx, senderID, recipientID, tx.CreatedAt)
	}
	return s.finalizeResult(ctx, tx, senderID, isNew)
}

// finalizeResult reconstructs a GiftResult from a posted (or
// idempotently-replayed) transaction, plus the sender's current balance —
// used both for a fresh send and for a retry that hit the idempotency
// short-circuit, so both paths return the same shape.
func (s *Service) finalizeResult(ctx context.Context, tx *ledger.Transaction, senderID string, isNew bool) (*GiftResult, error) {
	r := &GiftResult{TransactionID: tx.ID, IsNew: isNew}
	if giftID, ok := tx.Metadata["gift_id"].(string); ok {
		r.GiftID = giftID
	}
	if q, ok := tx.Metadata["quantity"].(int64); ok {
		r.Quantity = q
	}
	senderAcct := ledger.UserCoinsAccount(senderID)
	for _, e := range tx.Entries {
		switch {
		case e.AccountID == senderAcct && e.Currency == ledger.Coin:
			r.CoinsSpent = -e.Amount
		case e.AccountID == ledger.PlatformRevenueAccount && e.Currency == ledger.Coin:
			r.PlatformTook = e.Amount
		case e.Currency == ledger.Diamond && e.Amount > 0:
			r.CreatorDiamonds = e.Amount
		}
	}
	bal, err := s.repo.Balance(ctx, senderAcct, ledger.Coin)
	if err != nil {
		return nil, err
	}
	r.NewSenderBalance = bal
	return r, nil
}
