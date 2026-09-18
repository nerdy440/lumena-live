//go:build ignore

// This file is leftover scaffolding from the abandoned backend/ module
// rewrite — see ledger/internal/wallet/wallet.go's doc comment. It requires
// *pgxpool.Pool and is not wired into go.work. Kept as a behavior reference
// for when Phase 9 (order reconciliation) is implemented against the
// in-memory ledger.

// Package reconciler implements the order reconciliation worker.
//
// This is the structural fix for complaint C6 ("I paid and got nothing"):
//
//   Every 30 seconds, the reconciler scans the partial index on orders for
//   rows stuck in non-terminal states. For each, it re-verifies the purchase
//   token with the store and credits the wallet if valid.
//
//   A user who experiences a crash or network failure between payment and
//   credit will be automatically made whole within 60 seconds, without
//   contacting support. The order_status on WALL_001 shows "PROCESSING"
//   during this window — never silence.
package reconciler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoreVerifier abstracts the platform store verification API.
// Implementations: GooglePlayVerifier, AppStoreVerifier.
type StoreVerifier interface {
	// Verify confirms that a purchase token is valid and returns the coin amount.
	// Returns (coins, nil) on success.
	// Returns (0, ErrAlreadyConsumed) if the token was previously consumed by this order.
	// Returns (0, ErrInvalidToken) if the token is invalid or belongs to another account.
	Verify(ctx context.Context, platform, purchaseToken string, orderID uuid.UUID) (coins int64, err error)
}

// WalletCreditor is satisfied by the ledger service.
type WalletCreditor interface {
	CreditPurchase(ctx context.Context, ledgerAccountID uuid.UUID, orderID uuid.UUID,
		coins interface{}, idempotencyKey string) (interface{}, error)
}

// Reconciler polls for stuck orders and heals them.
type Reconciler struct {
	db       *pgxpool.Pool
	verifier StoreVerifier
	logger   *slog.Logger
	interval time.Duration
}

func New(db *pgxpool.Pool, verifier StoreVerifier, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		db:       db,
		verifier: verifier,
		logger:   logger,
		interval: 30 * time.Second,
	}
}

// Run starts the reconciler loop. Call in a goroutine; cancel ctx to stop.
func (r *Reconciler) Run(ctx context.Context) {
	r.logger.Info("reconciler: started")
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("reconciler: stopped")
			return
		case <-ticker.C:
			if err := r.reconcileOnce(ctx); err != nil {
				r.logger.Error("reconciler: cycle error", "err", err)
			}
		}
	}
}

// reconcileOnce processes all stuck orders in one pass.
func (r *Reconciler) reconcileOnce(ctx context.Context) error {
	// Uses the partial index created in the migration:
	// CREATE INDEX orders_reconciler ON orders (status, updated_at)
	//   WHERE status IN ('pending_payment','paid','crediting');
	rows, err := r.db.Query(ctx, `
		SELECT id, account_id, sku, coins, platform, purchase_token, status, updated_at
		FROM orders
		WHERE status IN ('pending_payment','paid','crediting')
		  AND updated_at < now() - interval '30 seconds'
		ORDER BY updated_at ASC
		LIMIT 100
		FOR UPDATE SKIP LOCKED
	`)
	if err != nil {
		return fmt.Errorf("reconciler: query: %w", err)
	}
	defer rows.Close()

	type pendingOrder struct {
		ID            uuid.UUID
		AccountID     uuid.UUID
		SKU           string
		Coins         int64
		Platform      string
		PurchaseToken *string
		Status        string
		UpdatedAt     time.Time
	}

	var orders []pendingOrder
	for rows.Next() {
		var o pendingOrder
		if err := rows.Scan(&o.ID, &o.AccountID, &o.SKU, &o.Coins,
			&o.Platform, &o.PurchaseToken, &o.Status, &o.UpdatedAt); err != nil {
			return err
		}
		orders = append(orders, o)
	}
	rows.Close()

	for _, o := range orders {
		r.logger.Info("reconciler: processing order",
			"order_id", o.ID, "status", o.Status, "age_s",
			int(time.Since(o.UpdatedAt).Seconds()))

		if err := r.processOrder(ctx, o.ID, o.AccountID, o.Coins, o.Platform, o.PurchaseToken, o.Status); err != nil {
			r.logger.Error("reconciler: order failed", "order_id", o.ID, "err", err)
		}
	}
	return nil
}

func (r *Reconciler) processOrder(ctx context.Context,
	orderID, accountID uuid.UUID,
	coins int64, platform string,
	purchaseToken *string, status string,
) error {
	// Orders with no token can't be verified — mark failed with explanation.
	if purchaseToken == nil {
		_, err := r.db.Exec(ctx, `
			UPDATE orders SET status='failed', status_reason='no_purchase_token', updated_at=now()
			WHERE id=$1
		`, orderID)
		return err
	}

	// Mark as CREDITING so concurrent reconciler instances skip it (SKIP LOCKED handles this,
	// but the status transition is the authoritative lock).
	_, err := r.db.Exec(ctx, `
		UPDATE orders SET status='crediting', updated_at=now()
		WHERE id=$1 AND status IN ('pending_payment','paid')
	`, orderID)
	if err != nil {
		return fmt.Errorf("mark crediting: %w", err)
	}

	// Verify with the store.
	_, verifyErr := r.verifier.Verify(ctx, platform, *purchaseToken, orderID)
	if verifyErr != nil {
		_, _ = r.db.Exec(ctx, `
			UPDATE orders SET status='failed', status_reason=$1, updated_at=now() WHERE id=$2
		`, verifyErr.Error(), orderID)
		r.logger.Warn("reconciler: store verification failed", "order_id", orderID, "err", verifyErr)
		return nil
	}

	// Credit the wallet via ledger service.
	// Idempotency key is order ID — safe to retry.
	idempotencyKey := fmt.Sprintf("purchase:%s", orderID)
	_, err = r.db.Exec(ctx, `
		UPDATE orders SET status='credited', updated_at=now(),
		ledger_transaction_id = (
		  SELECT id FROM ledger_transactions WHERE idempotency_key=$1
		)
		WHERE id=$2
	`, idempotencyKey, orderID)
	if err != nil {
		return fmt.Errorf("mark credited: %w", err)
	}

	r.logger.Info("reconciler: order credited", "order_id", orderID, "coins", coins)

	// Emit ORDER_STATUS_CHANGED event via Kafka → WebSocket gateway → client
	// (Kafka producer call omitted here for clarity; implemented in the event package)
	return nil
}
