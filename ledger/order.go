// Order machine for coin purchases (doc 06 §10, "the C6/C7 fix" — "I paid
// and got nothing"). Verify is the only path that credits coins; the order
// status is always visible, never silent, while a purchase is in flight.
package ledger

import (
	"context"
	"errors"
	"time"
)

type OrderStatus string

const (
	OrderCreated        OrderStatus = "created"
	OrderPendingPayment OrderStatus = "pending_payment"
	OrderPaid           OrderStatus = "paid"
	OrderCrediting      OrderStatus = "crediting"
	OrderCredited       OrderStatus = "credited"
	OrderFailed         OrderStatus = "failed"
	OrderRefunded       OrderStatus = "refunded"
)

// NonTerminalOrderStatuses is the reconciler's work queue predicate — doc
// 06 §10's partial index on orders (status, updated_at) WHERE status IN
// (pending_payment, paid, crediting).
var NonTerminalOrderStatuses = map[OrderStatus]bool{
	OrderPendingPayment: true,
	OrderPaid:           true,
	OrderCrediting:      true,
}

// Product is a purchasable coin SKU (doc 07 §6's GET /products).
type Product struct {
	SKU           string `json:"sku"`
	Coins         int64  `json:"coins"`
	PriceMinor    int64  `json:"price_minor"`
	PriceCurrency string `json:"price_currency"`
}

// Catalogue of dev SKUs. A real deployment loads region-priced SKUs from
// store config (doc 07 §6).
var Products = []Product{
	{SKU: "coins_100", Coins: 100, PriceMinor: 99, PriceCurrency: "USD"},
	{SKU: "coins_500", Coins: 500, PriceMinor: 499, PriceCurrency: "USD"},
	{SKU: "coins_2000", Coins: 2000, PriceMinor: 1999, PriceCurrency: "USD"},
}

func ProductBySKU(sku string) (Product, bool) {
	for _, p := range Products {
		if p.SKU == sku {
			return p, true
		}
	}
	return Product{}, false
}

// Order is one coin-purchase attempt.
type Order struct {
	ID                  string
	AccountID           string
	SKU                 string
	Coins               int64
	PriceMinor          int64
	PriceCurrency       string
	Platform            string
	PurchaseToken       string
	TokenHash           string // unique across ALL orders — prevents token replay across accounts
	Status              OrderStatus
	StatusReason        string
	LedgerTransactionID string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

var (
	ErrOrderNotFound       = errors.New("ledger: order not found")
	ErrUnknownSKU          = errors.New("ledger: unknown product sku")
	ErrTokenAlreadyUsed    = errors.New("ledger: purchase token already consumed by another order")
	ErrOrderNotOwned       = errors.New("ledger: order does not belong to this account")
	ErrOrderTerminal       = errors.New("ledger: order is already in a terminal state")
)

// OrderRepo is the orders storage contract.
type OrderRepo interface {
	CreateOrder(ctx context.Context, accountID, sku string) (*Order, error)
	GetOrder(ctx context.Context, id string) (*Order, error)
	// FindByTokenHash returns the order that already consumed tokenHash, if
	// any — the replay-prevention check (doc 06 §10's UNIQUE token_hash).
	FindByTokenHash(ctx context.Context, tokenHash string) (*Order, error)
	ListOrders(ctx context.Context, accountID string, status OrderStatus) ([]Order, error)
	// ListNonTerminal returns every order across all accounts in a
	// non-terminal state — the reconciler's work queue.
	ListNonTerminal(ctx context.Context) ([]Order, error)
	// UpdateOrder persists a full order record (compare-and-swap free —
	// callers hold the only reference during their critical section, same
	// pattern as MemLedger).
	UpdateOrder(ctx context.Context, order *Order) error
}
