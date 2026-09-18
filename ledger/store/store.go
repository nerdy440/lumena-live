// Package store abstracts platform store purchase verification (doc 07
// §6's "server verifies with store"). The order's coin amount is already
// fixed by the SKU at order-creation time; Verify only confirms the
// purchase token is genuine, unconsumed, and paid.
package store

import (
	"context"
	"errors"
	"strings"
	"sync"
)

var (
	// ErrPending means the store hasn't confirmed payment yet — the caller
	// must leave the order in a non-terminal status and let the reconciler
	// retry, never silently drop it (doc 07 §6, doc 12 Phase 9 exit gate).
	ErrPending = errors.New("store: payment not yet confirmed")
	// ErrInvalidToken means the token is malformed, expired, or was already
	// consumed at the store level.
	ErrInvalidToken = errors.New("store: invalid purchase token")
)

// PurchaseExpectation is what the order system already knows about a
// purchase before asking the store to confirm it — a real Verifier must
// check the store's answer against these, not just "was this token ever
// valid for anything." Without this, a client could buy the cheapest SKU,
// then replay that genuine-but-cheap token against an order for an
// expensive SKU and receive coins it never paid for.
type PurchaseExpectation struct {
	OrderID       string // matched against the store's client/order reference
	PriceMinor    int64
	PriceCurrency string
}

// Verifier confirms a purchase token with the platform store, and that the
// store's own record of the purchase matches what this order expects.
type Verifier interface {
	Verify(ctx context.Context, platform, purchaseToken string, expected PurchaseExpectation) error
}

// DevVerifier is a deterministic stand-in for GooglePlayVerifier/
// AppStoreVerifier — NEVER ships to production, same as
// streaming.DevIngestService and translate's DevTranslationProvider.
// Behavior is driven by the token's prefix so tests and the dev UI can
// exercise every path without a real store account:
//   - "fail-*"    -> ErrInvalidToken, always
//   - "pending-*" -> ErrPending for the first two calls, then succeeds
//                    (simulates the store settling asynchronously, which is
//                    exactly what the reconciler exists to ride out)
//   - anything else -> succeeds immediately
type DevVerifier struct {
	mu       sync.Mutex
	attempts map[string]int
}

func NewDevVerifier() *DevVerifier {
	return &DevVerifier{attempts: make(map[string]int)}
}

func (v *DevVerifier) Verify(_ context.Context, _, purchaseToken string, _ PurchaseExpectation) error {
	if strings.HasPrefix(purchaseToken, "fail-") {
		return ErrInvalidToken
	}
	if strings.HasPrefix(purchaseToken, "pending-") {
		v.mu.Lock()
		v.attempts[purchaseToken]++
		n := v.attempts[purchaseToken]
		v.mu.Unlock()
		if n < 3 {
			return ErrPending
		}
	}
	return nil
}
