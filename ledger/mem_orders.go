package ledger

import (
	"context"
	"sync"
	"time"
)

// MemOrderRepo is the in-memory dev/test OrderRepo implementation.
type MemOrderRepo struct {
	mu        sync.Mutex
	byID      map[string]*Order
	byToken   map[string]string // token hash -> order id
	byAccount map[string][]string
	seq       int
}

func NewMemOrderRepo() *MemOrderRepo {
	return &MemOrderRepo{
		byID:      make(map[string]*Order),
		byToken:   make(map[string]string),
		byAccount: make(map[string][]string),
	}
}

var _ OrderRepo = (*MemOrderRepo)(nil)

func (r *MemOrderRepo) CreateOrder(_ context.Context, accountID, sku, platform string) (*Order, error) {
	product, ok := ProductBySKU(sku)
	if !ok {
		return nil, ErrUnknownSKU
	}
	if platform == "" {
		platform = PlatformDevStore
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	now := time.Now()
	o := &Order{
		ID:            "order-" + itoaLedger(r.seq),
		AccountID:     accountID,
		SKU:           sku,
		Coins:         product.Coins,
		PriceMinor:    product.PriceMinor,
		PriceCurrency: product.PriceCurrency,
		Platform:      platform,
		Status:        OrderCreated,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	r.byID[o.ID] = o
	r.byAccount[accountID] = append(r.byAccount[accountID], o.ID)
	cp := *o
	return &cp, nil
}

func (r *MemOrderRepo) GetOrder(_ context.Context, id string) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.byID[id]
	if !ok {
		return nil, ErrOrderNotFound
	}
	cp := *o
	return &cp, nil
}

func (r *MemOrderRepo) FindByTokenHash(_ context.Context, tokenHash string) (*Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byToken[tokenHash]
	if !ok {
		return nil, nil
	}
	cp := *r.byID[id]
	return &cp, nil
}

func (r *MemOrderRepo) ListOrders(_ context.Context, accountID string, status OrderStatus) ([]Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Order
	for _, id := range r.byAccount[accountID] {
		o := r.byID[id]
		if status == "" || o.Status == status {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (r *MemOrderRepo) ListNonTerminal(_ context.Context) ([]Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Order
	for _, o := range r.byID {
		if NonTerminalOrderStatuses[o.Status] {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (r *MemOrderRepo) UpdateOrder(_ context.Context, order *Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[order.ID]; !ok {
		return ErrOrderNotFound
	}
	order.UpdatedAt = time.Now()
	cp := *order
	r.byID[order.ID] = &cp
	if order.TokenHash != "" {
		r.byToken[order.TokenHash] = order.ID
	}
	return nil
}
