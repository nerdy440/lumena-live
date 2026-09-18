// Package ordersvc implements the coin-purchase order machine (doc 06 §10,
// doc 07 §6, roadmap Phase 9): created → pending_payment → paid →
// crediting → credited, with Verify as the only path that credits coins.
package ordersvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/lumena/ledger"
	"github.com/lumena/ledger/store"
)

// DisputeHook is notified after a dispute is opened — the fraud module's
// chargeback tracker (Phase 18, AF-05) wires this to flag repeat disputers.
// Optional.
type DisputeHook func(ctx context.Context, accountID, orderID string)

type Service struct {
	orders   ledger.OrderRepo
	ledger   ledger.Repo
	verifier store.Verifier
	limits   ledger.SpendLimitsRepo // optional — nil disables spend-limit enforcement
	onDispute DisputeHook

	mu           sync.Mutex
	createByKey  map[string]*ledger.Order // idempotency key -> order, for POST /orders
}

func NewService(orders ledger.OrderRepo, ledgerRepo ledger.Repo, verifier store.Verifier) *Service {
	return &Service{
		orders: orders, ledger: ledgerRepo, verifier: verifier,
		createByKey: make(map[string]*ledger.Order),
	}
}

// WithDisputeHook registers the fraud module's chargeback tracker.
func (s *Service) WithDisputeHook(hook DisputeHook) *Service {
	s.onDispute = hook
	return s
}

// WithSpendLimits enables cooling-off and daily/weekly/monthly cap
// enforcement on CreateOrder (doc 11 §9, roadmap Phase 9).
func (s *Service) WithSpendLimits(limits ledger.SpendLimitsRepo) *Service {
	s.limits = limits
	return s
}

// CreateOrder starts a purchase for sku, idempotent on idempotencyKey (doc
// 07 §6: "POST /orders — Idempotent").
func (s *Service) CreateOrder(ctx context.Context, accountID, sku, idempotencyKey string) (*ledger.Order, error) {
	s.mu.Lock()
	if existing, ok := s.createByKey[idempotencyKey]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.mu.Unlock()

	product, ok := ledger.ProductBySKU(sku)
	if !ok {
		return nil, ledger.ErrUnknownSKU
	}
	if err := s.checkSpendLimits(ctx, accountID, product.Coins); err != nil {
		return nil, err
	}

	order, err := s.orders.CreateOrder(ctx, accountID, sku)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.createByKey[idempotencyKey] = order
	s.mu.Unlock()
	return order, nil
}

// checkSpendLimits enforces cooling-off and the daily/weekly/monthly coin
// caps against coins already purchased (credited or in-flight) in each
// window, so a purchase that would immediately exceed the cap is rejected
// up front rather than after the user has already paid the store.
func (s *Service) checkSpendLimits(ctx context.Context, accountID string, coins int64) error {
	if s.limits == nil {
		return nil
	}
	limits, err := s.limits.Get(ctx, accountID)
	if err != nil {
		return err
	}
	limits = ledger.ResolvePending(limits, time.Now())
	if limits.CoolingOff && time.Now().Before(limits.CoolingOffUntil) {
		return ledger.ErrCoolingOff
	}

	windows := []struct {
		cap    int64
		since  time.Duration
	}{
		{limits.DailyCap, 24 * time.Hour},
		{limits.WeeklyCap, 7 * 24 * time.Hour},
		{limits.MonthlyCap, 30 * 24 * time.Hour},
	}
	for _, w := range windows {
		if w.cap <= 0 {
			continue
		}
		spent, err := s.coinsPurchasedSince(ctx, accountID, time.Now().Add(-w.since))
		if err != nil {
			return err
		}
		if spent+coins > w.cap {
			return ledger.ErrSpendLimitExceeded
		}
	}
	return nil
}

// coinsPurchasedSince sums this account's non-terminal-or-credited orders'
// coin amounts created since the given time — a simple, dev-scale stand-in
// for a real windowed aggregate query.
func (s *Service) coinsPurchasedSince(ctx context.Context, accountID string, since time.Time) (int64, error) {
	orders, err := s.orders.ListOrders(ctx, accountID, "")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, o := range orders {
		if o.Status == ledger.OrderFailed || o.Status == ledger.OrderRefunded {
			continue
		}
		if o.CreatedAt.Before(since) {
			continue
		}
		total += o.Coins
	}
	return total, nil
}

func (s *Service) GetOrder(ctx context.Context, accountID, orderID string) (*ledger.Order, error) {
	order, err := s.orders.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.AccountID != accountID {
		return nil, ledger.ErrOrderNotOwned
	}
	return order, nil
}

func (s *Service) ListOrders(ctx context.Context, accountID string, status ledger.OrderStatus) ([]ledger.Order, error) {
	return s.orders.ListOrders(ctx, accountID, status)
}

// VerifyOrder is the only path that credits coins (doc 07 §6). It is safe
// to call more than once for the same order — by a retrying client, or by
// the reconciler recovering a crashed attempt — because every step is
// checkpointed to the order's persisted status, and the final ledger post
// uses a deterministic idempotency key derived from the order id, so a
// re-entrant crediting attempt can never double-credit.
func (s *Service) VerifyOrder(ctx context.Context, accountID, orderID, purchaseToken string) (*ledger.Order, error) {
	order, err := s.orders.GetOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.AccountID != accountID {
		return nil, ledger.ErrOrderNotOwned
	}
	return s.advance(ctx, order, purchaseToken)
}

// advance runs (or re-runs) the verify→credit pipeline for order. Used by
// both VerifyOrder (first attempt, has the token) and the reconciler
// (retries using the token already stored on the order).
func (s *Service) advance(ctx context.Context, order *ledger.Order, purchaseToken string) (*ledger.Order, error) {
	if order.Status == ledger.OrderCredited {
		return order, nil // idempotent replay
	}
	if order.Status == ledger.OrderFailed || order.Status == ledger.OrderRefunded {
		return nil, ledger.ErrOrderTerminal
	}

	if purchaseToken == "" {
		purchaseToken = order.PurchaseToken
	}
	tokenHash := hashToken(purchaseToken)

	// Replay prevention (doc 06 §10's UNIQUE token_hash / roadmap exit gate:
	// "purchase token cannot be replayed to a second account").
	if existing, _ := s.orders.FindByTokenHash(ctx, tokenHash); existing != nil && existing.ID != order.ID {
		order.Status = ledger.OrderFailed
		order.StatusReason = "purchase token already consumed by another order"
		_ = s.orders.UpdateOrder(ctx, order)
		return nil, ledger.ErrTokenAlreadyUsed
	}

	if order.Status == ledger.OrderCreated {
		order.PurchaseToken = purchaseToken
		order.TokenHash = tokenHash
		order.Status = ledger.OrderPendingPayment
		if err := s.orders.UpdateOrder(ctx, order); err != nil {
			return nil, err
		}
	}

	if order.Status == ledger.OrderPendingPayment {
		err := s.verifier.Verify(ctx, order.Platform, purchaseToken, store.PurchaseExpectation{
			OrderID:       order.ID,
			PriceMinor:    order.PriceMinor,
			PriceCurrency: order.PriceCurrency,
		})
		switch {
		case errors.Is(err, store.ErrPending):
			order.StatusReason = "waiting for the store to confirm payment"
			_ = s.orders.UpdateOrder(ctx, order)
			return order, nil // not an error — client sees status, not silence
		case errors.Is(err, store.ErrInvalidToken):
			order.Status = ledger.OrderFailed
			order.StatusReason = "invalid purchase token"
			_ = s.orders.UpdateOrder(ctx, order)
			return nil, err
		case err != nil:
			return nil, err
		}
		order.Status = ledger.OrderPaid
		order.StatusReason = "payment confirmed — crediting your account"
		if err := s.orders.UpdateOrder(ctx, order); err != nil {
			return nil, err
		}
	}

	if order.Status == ledger.OrderPaid {
		order.Status = ledger.OrderCrediting
		if err := s.orders.UpdateOrder(ctx, order); err != nil {
			return nil, err
		}
		// This is the exact window the roadmap's exit gate targets: "kill
		// server after store purchase but before credit; confirm reconciler
		// credits within 60s." If the process dies right here, the order
		// is left at "crediting" — the reconciler's work queue — and
		// resuming just re-enters this branch.
	}

	if order.Status == ledger.OrderCrediting {
		// Convention: a platform account entry offsets whichever direction
		// keeps the transaction net-zero for COIN — see ledger.go's doc
		// comment on platform account semantics. Issuing coins to a user
		// is a credit to their wallet, offset here by a debit to
		// platform:liability (the platform's outstanding obligation grows
		// more negative as it issues more spendable value).
		tx, _, err := s.ledger.PostTransaction(ctx, "purchase", "order-"+order.ID, map[string]any{
			"order_id": order.ID, "sku": order.SKU, "account_id": order.AccountID,
		}, []ledger.Entry{
			{AccountID: ledger.UserCoinsAccount(order.AccountID), Amount: order.Coins, Currency: ledger.Coin},
			{AccountID: ledger.PlatformLiabilityAccount, Amount: -order.Coins, Currency: ledger.Coin},
		})
		if err != nil {
			return nil, err
		}
		order.Status = ledger.OrderCredited
		order.StatusReason = "coins credited"
		order.LedgerTransactionID = tx.ID
		if err := s.orders.UpdateOrder(ctx, order); err != nil {
			return nil, err
		}
	}

	return order, nil
}

// Reconcile re-attempts advance() for one order using its already-stored
// token — the reconciler's per-order recovery step.
func (s *Service) Reconcile(ctx context.Context, order *ledger.Order) (*ledger.Order, error) {
	return s.advance(ctx, order, "")
}

// ListNonTerminal exposes the reconciler's work queue (doc 06 §10's partial
// index on orders WHERE status IN (...)).
func (s *Service) ListNonTerminal(ctx context.Context) ([]ledger.Order, error) {
	return s.orders.ListNonTerminal(ctx)
}

// Dispute opens a support case for a stuck or contested order (doc 07 §6's
// POST /orders/{id}/dispute). No real support-ticket system exists in this
// dev build (roadmap Phase 0's SUPP_002/003 in-app tickets are out of
// scope here) — this returns a stable case reference so the client can at
// least surface one, matching the "never silence" principle.
func (s *Service) Dispute(ctx context.Context, accountID, orderID string) (caseID string, err error) {
	order, err := s.GetOrder(ctx, accountID, orderID)
	if err != nil {
		return "", err
	}
	if s.onDispute != nil {
		s.onDispute(ctx, accountID, order.ID)
	}
	return "ORD-" + order.ID, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
