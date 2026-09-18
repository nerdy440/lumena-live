//go:build ignore

// This file is leftover scaffolding from the abandoned backend/ module
// rewrite (see repo investigation: backend/ is not referenced by go.work,
// predates the current auth/profile/feed/streaming tree, and was never
// wired up). It requires a live *pgxpool.Pool and github.com/lumena/backend
// — neither exists in this workspace — so it cannot compile as part of the
// ledger module built for Phase 8/9. Kept only as a schema/behavior
// reference; the actual implementation is ledger/mem_ledger.go, which
// follows the same double-entry design against an in-memory store, matching
// every other module in this workspace (auth, profile, feed, chat).

// Package wallet implements the server-authoritative double-entry ledger.
// Invariants enforced here AND at the database level:
//   - Balances are never negative (CHECK constraint)
//   - Transactions are idempotent (UNIQUE on idempotency_key)
//   - Entries are append-only (no UPDATE/DELETE at the DB role level)
//   - No float64 touches money
//   - The client NEVER credits its own wallet
package wallet

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/backend/pkg/money"
)

// TransactionKind distinguishes ledger transaction types for audit and display.
type TransactionKind string

const (
	KindPurchase   TransactionKind = "purchase"
	KindGift       TransactionKind = "gift"
	KindPVHold     TransactionKind = "pv_hold"
	KindPVSettle   TransactionKind = "pv_settle"
	KindRefund     TransactionKind = "refund"
	KindPayout     TransactionKind = "payout"
	KindPromo      TransactionKind = "promo"
	KindAdjustment TransactionKind = "adjustment"
)

// LedgerService is the sole authority on wallet balances.
type LedgerService struct {
	db *pgxpool.Pool

	// Platform account IDs (seeded at startup)
	platformRevenueAccountID  uuid.UUID
	platformLiabilityAccountID uuid.UUID
	paymentClearingAccountID   uuid.UUID

	// Economy parameters (loaded from config, not hardcoded)
	platformTakeBPS         int64  // e.g. 3000 = 30%
	diamondsPerThousandCoins int64 // e.g. 700 = 0.7 diamond per coin
}

// Balance returns the current authoritative balance for an account.
// This value is always from the primary — never a replica.
func (s *LedgerService) Balance(ctx context.Context, ledgerAccountID uuid.UUID) (money.Amount, error) {
	var bal int64
	var curr string
	err := s.db.QueryRow(ctx, `
		SELECT wb.balance, la.currency
		FROM wallet_balances wb
		JOIN ledger_accounts la ON la.id = wb.ledger_account_id
		WHERE wb.ledger_account_id = $1
	`, ledgerAccountID).Scan(&bal, &curr)
	if err == pgx.ErrNoRows {
		return money.Amount{}, nil
	}
	if err != nil {
		return money.Amount{}, fmt.Errorf("ledger: balance: %w", err)
	}
	return money.Amount{Value: bal, Currency: money.Currency(curr)}, nil
}

// GiftResult is returned from a successful SendGift call.
type GiftResult struct {
	TransactionID uuid.UUID
	NewBalance    money.Amount
	CoinsSpent    money.Amount
	CreatorEarned money.Amount // in diamonds
	PlatformTook  money.Amount // in coins
}

// SendGift atomically debits the sender's coin wallet and credits the creator's diamond ledger.
// Idempotency: if idempotencyKey has already been processed, returns the original result.
// No float arithmetic. No optimistic updates. No client trust.
func (s *LedgerService) SendGift(ctx context.Context,
	senderLedgerAccountID uuid.UUID,
	creatorLedgerAccountID uuid.UUID,
	roomID uuid.UUID,
	giftID string,
	coinCost money.Amount,
	idempotencyKey string,
) (*GiftResult, error) {

	// Check idempotency before acquiring locks.
	existing, err := s.findExistingTransaction(ctx, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	// Compute shares using integer arithmetic only.
	_, platformShare, err := money.ApplyPlatformTake(coinCost, s.platformTakeBPS)
	if err != nil {
		return nil, fmt.Errorf("ledger: gift: compute share: %w", err)
	}
	creatorCoins, _, _ := money.ApplyPlatformTake(coinCost, s.platformTakeBPS)

	creatorDiamonds, err := money.CoinsToDiamonds(creatorCoins, s.diamondsPerThousandCoins)
	if err != nil {
		return nil, fmt.Errorf("ledger: gift: convert: %w", err)
	}

	var result GiftResult

	// SERIALIZABLE transaction — the only isolation level acceptable for money.
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		// Acquire balance with FOR UPDATE to prevent concurrent overdraft.
		var balance int64
		err := tx.QueryRow(ctx, `
			SELECT wb.balance FROM wallet_balances wb
			WHERE wb.ledger_account_id = $1
			FOR UPDATE
		`, senderLedgerAccountID).Scan(&balance)
		if err != nil {
			return fmt.Errorf("lock balance: %w", err)
		}

		if balance < coinCost.Value {
			return &InsufficientBalanceError{Required: coinCost.Value, Available: balance}
		}

		// Insert the transaction record.
		txID := uuid.New()
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_transactions (id, kind, idempotency_key, metadata)
			VALUES ($1, $2, $3, $4)
		`, txID, KindGift, idempotencyKey, map[string]any{
			"gift_id": giftID, "room_id": roomID.String(),
			"sender_id": senderLedgerAccountID.String(),
		})
		if err != nil {
			return fmt.Errorf("insert transaction: %w", err)
		}

		// Insert entries — these sum to zero per currency.
		entries := []struct {
			accountID uuid.UUID
			amount    int64
			currency  string
		}{
			{senderLedgerAccountID, -coinCost.Value, string(money.Coin)},
			{creatorLedgerAccountID, creatorDiamonds.Value, string(money.Diamond)},
			{s.platformRevenueAccountID, platformShare.Value, string(money.Coin)},
			{s.platformLiabilityAccountID, -creatorCoins.Value, string(money.Coin)},
		}

		for _, e := range entries {
			_, err = tx.Exec(ctx, `
				INSERT INTO ledger_entries (transaction_id, account_id, amount, currency)
				VALUES ($1, $2, $3, $4)
			`, txID, e.accountID, e.amount, e.currency)
			if err != nil {
				return fmt.Errorf("insert entry: %w", err)
			}
		}

		// Update materialized balances atomically.
		newBalance := balance - coinCost.Value
		_, err = tx.Exec(ctx, `
			UPDATE wallet_balances SET balance=$1, version=version+1, updated_at=now()
			WHERE ledger_account_id=$2
		`, newBalance, senderLedgerAccountID)
		if err != nil {
			return fmt.Errorf("update balance: %w", err)
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO wallet_balances (ledger_account_id, balance, version)
			VALUES ($1, $2, 1)
			ON CONFLICT (ledger_account_id) DO UPDATE
			SET balance=wallet_balances.balance+$2, version=wallet_balances.version+1, updated_at=now()
		`, creatorLedgerAccountID, creatorDiamonds.Value)
		if err != nil {
			return fmt.Errorf("update creator balance: %w", err)
		}

		result = GiftResult{
			TransactionID: txID,
			NewBalance:    money.Coins(newBalance),
			CoinsSpent:    coinCost,
			CreatorEarned: creatorDiamonds,
			PlatformTook:  platformShare,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &result, nil
}

// CreditPurchase credits coins to a viewer's wallet after a verified store purchase.
// Called only after the server has independently verified the purchase token with the store.
// Idempotent on idempotencyKey.
func (s *LedgerService) CreditPurchase(ctx context.Context,
	viewerLedgerAccountID uuid.UUID,
	orderID uuid.UUID,
	coins money.Amount,
	idempotencyKey string,
) (money.Amount, error) {
	existing, err := s.findExistingTransaction(ctx, idempotencyKey)
	if err != nil {
		return money.Amount{}, err
	}
	if existing != nil {
		return existing.NewBalance, nil
	}

	var newBalance int64
	err = pgx.BeginTxFunc(ctx, s.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		txID := uuid.New()
		_, err := tx.Exec(ctx, `
			INSERT INTO ledger_transactions (id, kind, idempotency_key, metadata)
			VALUES ($1, $2, $3, $4)
		`, txID, KindPurchase, idempotencyKey, map[string]any{"order_id": orderID.String()})
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_entries (transaction_id, account_id, amount, currency)
			VALUES ($1, $2, $3, $4)
		`, txID, viewerLedgerAccountID, coins.Value, string(coins.Currency))
		if err != nil {
			return err
		}

		// Credit platform liability (we owe this as a float to users)
		_, err = tx.Exec(ctx, `
			INSERT INTO ledger_entries (transaction_id, account_id, amount, currency)
			VALUES ($1, $2, $3, $4)
		`, txID, s.platformLiabilityAccountID, coins.Value, string(coins.Currency))
		if err != nil {
			return err
		}

		err = tx.QueryRow(ctx, `
			INSERT INTO wallet_balances (ledger_account_id, balance, version)
			VALUES ($1, $2, 1)
			ON CONFLICT (ledger_account_id) DO UPDATE
			SET balance=wallet_balances.balance+$2, version=wallet_balances.version+1, updated_at=now()
			RETURNING balance
		`, viewerLedgerAccountID, coins.Value).Scan(&newBalance)
		return err
	})

	if err != nil {
		return money.Amount{}, fmt.Errorf("ledger: credit purchase: %w", err)
	}

	// Update order to CREDITED
	_, _ = s.db.Exec(ctx, `
		UPDATE orders SET status='credited', updated_at=now() WHERE id=$1
	`, orderID)

	return money.Coins(newBalance), nil
}

// TransactionHistory returns the ledger entries for a viewer's account,
// formatted for display. Every balance delta is visible here — the BT-07 implementation.
func (s *LedgerService) TransactionHistory(ctx context.Context,
	ledgerAccountID uuid.UUID,
	cursor time.Time,
	limit int,
) ([]TransactionHistoryItem, string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	rows, err := s.db.Query(ctx, `
		SELECT le.id, lt.kind, le.amount, le.currency, lt.metadata, le.created_at
		FROM ledger_entries le
		JOIN ledger_transactions lt ON lt.id = le.transaction_id
		WHERE le.account_id = $1 AND le.created_at < $2
		ORDER BY le.created_at DESC
		LIMIT $3
	`, ledgerAccountID, cursor, limit+1)
	if err != nil {
		return nil, "", fmt.Errorf("ledger: history: %w", err)
	}
	defer rows.Close()

	var items []TransactionHistoryItem
	for rows.Next() {
		var item TransactionHistoryItem
		var meta map[string]any
		if err := rows.Scan(&item.ID, &item.Kind, &item.Amount, &item.Currency, &meta, &item.CreatedAt); err != nil {
			return nil, "", err
		}
		item.Metadata = meta
		items = append(items, item)
	}

	var nextCursor string
	if len(items) > limit {
		t, _ := items[limit-1].CreatedAt.MarshalText()
		nextCursor = string(t)
		items = items[:limit]
	}

	return items, nextCursor, nil
}

// TransactionHistoryItem is one row in the user-visible ledger — BT-07.
type TransactionHistoryItem struct {
	ID        int64
	Kind      TransactionKind
	Amount    int64
	Currency  string
	Metadata  map[string]any
	CreatedAt time.Time
}

// InsufficientBalanceError carries the exact shortfall for the 402 response.
type InsufficientBalanceError struct {
	Required  int64
	Available int64
}

func (e *InsufficientBalanceError) Error() string {
	return fmt.Sprintf("insufficient balance: need %d, have %d (shortfall %d)",
		e.Required, e.Available, e.Required-e.Available)
}
func (e *InsufficientBalanceError) Shortfall() int64 { return e.Required - e.Available }

// findExistingTransaction looks up a completed transaction by idempotency key.
// Returns nil if not found.
func (s *LedgerService) findExistingTransaction(ctx context.Context, key string) (*GiftResult, error) {
	// Fast path: check Redis cache first (populated when the transaction was created)
	// For now, fall through to DB
	var txID uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT id FROM ledger_transactions WHERE idempotency_key=$1
	`, key).Scan(&txID)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("ledger: idempotency check: %w", err)
	}
	// Transaction exists — return a placeholder result
	// (full reconstruction would require joining back through entries)
	return &GiftResult{TransactionID: txID}, nil
}
