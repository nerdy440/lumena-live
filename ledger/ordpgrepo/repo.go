// Package ordpgrepo implements ledger.OrderRepo and ledger.SpendLimitsRepo
// against real Postgres — the production counterpart to
// ledger.MemOrderRepo/MemSpendLimitsRepo. Separate from ledger/pgrepo
// (which covers the core double-entry ledger) since orders/spend-limits
// are a distinct storage concern with their own tables.
package ordpgrepo

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/ledger"
)

// ─── Orders ─────────────────────────────────────────────────────────────────

type OrderRepo struct {
	pool *pgxpool.Pool
}

func NewOrderRepo(pool *pgxpool.Pool) *OrderRepo {
	return &OrderRepo{pool: pool}
}

var _ ledger.OrderRepo = (*OrderRepo)(nil)

const orderCols = `id, account_id, sku, coins, price_minor, price_currency, platform, purchase_token,
	token_hash, status, status_reason, ledger_transaction_id, created_at, updated_at`

func scanOrder(row pgx.Row) (*ledger.Order, error) {
	var o ledger.Order
	var status string
	err := row.Scan(&o.ID, &o.AccountID, &o.SKU, &o.Coins, &o.PriceMinor, &o.PriceCurrency, &o.Platform, &o.PurchaseToken,
		&o.TokenHash, &status, &o.StatusReason, &o.LedgerTransactionID, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ledger.ErrOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	o.Status = ledger.OrderStatus(status)
	return &o, nil
}

func (r *OrderRepo) CreateOrder(ctx context.Context, accountID, sku string) (*ledger.Order, error) {
	product, ok := ledger.ProductBySKU(sku)
	if !ok {
		return nil, ledger.ErrUnknownSKU
	}
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('ledger_order_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	id := "order-" + strconv.FormatInt(seq, 10)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO ledger_orders (id, account_id, sku, coins, price_minor, price_currency, platform, status)
		VALUES ($1, $2, $3, $4, $5, $6, 'dev_store', $7)
		RETURNING `+orderCols,
		id, accountID, sku, product.Coins, product.PriceMinor, product.PriceCurrency, string(ledger.OrderCreated))
	return scanOrder(row)
}

func (r *OrderRepo) GetOrder(ctx context.Context, id string) (*ledger.Order, error) {
	return scanOrder(r.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM ledger_orders WHERE id = $1`, id))
}

func (r *OrderRepo) FindByTokenHash(ctx context.Context, tokenHash string) (*ledger.Order, error) {
	o, err := scanOrder(r.pool.QueryRow(ctx, `SELECT `+orderCols+` FROM ledger_orders WHERE token_hash = $1`, tokenHash))
	if errors.Is(err, ledger.ErrOrderNotFound) {
		return nil, nil
	}
	return o, err
}

func (r *OrderRepo) ListOrders(ctx context.Context, accountID string, status ledger.OrderStatus) ([]ledger.Order, error) {
	var rows pgx.Rows
	var err error
	if status == "" {
		rows, err = r.pool.Query(ctx, `SELECT `+orderCols+` FROM ledger_orders WHERE account_id = $1`, accountID)
	} else {
		rows, err = r.pool.Query(ctx, `SELECT `+orderCols+` FROM ledger_orders WHERE account_id = $1 AND status = $2`, accountID, string(status))
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (r *OrderRepo) ListNonTerminal(ctx context.Context) ([]ledger.Order, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+orderCols+` FROM ledger_orders WHERE status IN ('pending_payment', 'paid', 'crediting')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ledger.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (r *OrderRepo) UpdateOrder(ctx context.Context, order *ledger.Order) error {
	order.UpdatedAt = time.Now()
	var tokenHash any
	if order.TokenHash != "" {
		tokenHash = order.TokenHash
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE ledger_orders SET
			purchase_token = $2, token_hash = COALESCE($3, token_hash), status = $4, status_reason = $5,
			ledger_transaction_id = $6, updated_at = $7
		WHERE id = $1`,
		order.ID, order.PurchaseToken, tokenHash, string(order.Status), order.StatusReason,
		order.LedgerTransactionID, order.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ledger.ErrOrderNotFound
	}
	return nil
}

// ─── Spend limits ───────────────────────────────────────────────────────────

type SpendLimitsRepo struct {
	pool *pgxpool.Pool
}

func NewSpendLimitsRepo(pool *pgxpool.Pool) *SpendLimitsRepo {
	return &SpendLimitsRepo{pool: pool}
}

var _ ledger.SpendLimitsRepo = (*SpendLimitsRepo)(nil)

func (r *SpendLimitsRepo) Get(ctx context.Context, accountID string) (*ledger.SpendLimits, error) {
	var l ledger.SpendLimits
	var coolingOffUntil, pendingEffectiveAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT account_id, daily_cap, weekly_cap, monthly_cap, cooling_off, cooling_off_until,
			pending_daily_cap, pending_weekly_cap, pending_monthly_cap, pending_effective_at
		FROM ledger_spend_limits WHERE account_id = $1`, accountID).Scan(
		&l.AccountID, &l.DailyCap, &l.WeeklyCap, &l.MonthlyCap, &l.CoolingOff, &coolingOffUntil,
		&l.PendingDailyCap, &l.PendingWeeklyCap, &l.PendingMonthlyCap, &pendingEffectiveAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &ledger.SpendLimits{AccountID: accountID}, nil
	}
	if err != nil {
		return nil, err
	}
	if coolingOffUntil != nil {
		l.CoolingOffUntil = *coolingOffUntil
	}
	if pendingEffectiveAt != nil {
		l.PendingEffectiveAt = *pendingEffectiveAt
	}
	return &l, nil
}

func (r *SpendLimitsRepo) Set(ctx context.Context, limits *ledger.SpendLimits) error {
	var coolingOffUntil, pendingEffectiveAt *time.Time
	if !limits.CoolingOffUntil.IsZero() {
		coolingOffUntil = &limits.CoolingOffUntil
	}
	if !limits.PendingEffectiveAt.IsZero() {
		pendingEffectiveAt = &limits.PendingEffectiveAt
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO ledger_spend_limits (account_id, daily_cap, weekly_cap, monthly_cap, cooling_off, cooling_off_until,
			pending_daily_cap, pending_weekly_cap, pending_monthly_cap, pending_effective_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (account_id) DO UPDATE SET
			daily_cap = EXCLUDED.daily_cap, weekly_cap = EXCLUDED.weekly_cap, monthly_cap = EXCLUDED.monthly_cap,
			cooling_off = EXCLUDED.cooling_off, cooling_off_until = EXCLUDED.cooling_off_until,
			pending_daily_cap = EXCLUDED.pending_daily_cap, pending_weekly_cap = EXCLUDED.pending_weekly_cap,
			pending_monthly_cap = EXCLUDED.pending_monthly_cap, pending_effective_at = EXCLUDED.pending_effective_at`,
		limits.AccountID, limits.DailyCap, limits.WeeklyCap, limits.MonthlyCap, limits.CoolingOff, coolingOffUntil,
		limits.PendingDailyCap, limits.PendingWeeklyCap, limits.PendingMonthlyCap, pendingEffectiveAt)
	return err
}
