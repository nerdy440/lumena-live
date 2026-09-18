// Package ledger implements a server-authoritative double-entry ledger
// (doc 06 §9, doc 11 §2-3): every balance change is a transaction whose
// entries sum to zero per currency, transactions are idempotent on a
// client-supplied key, and balances are materialized integers — no float64
// anywhere near money, matching the architecture's hard constraint.
package ledger

import (
	"context"
	"errors"
	"time"
)

type Currency string

const (
	Coin    Currency = "COIN"
	Diamond Currency = "DIAMOND"
)

// Well-known platform account IDs (doc 06 §9's platform_revenue,
// platform_liability account kinds). Diamond issuance is balanced against
// PlatformDiamondLiabilityAccount so DIAMOND, like COIN, always sums to
// zero across the ledger — doc 11 §3's worked example leaves the DIAMOND
// leg unbalanced ("DIAMOND sum: +350 ← platform owes creator..."); this
// adds the offsetting liability entry so the invariant actually holds,
// rather than treating diamond issuance as an ungrounded liability.
const (
	PlatformRevenueAccount         = "platform:revenue"
	PlatformLiabilityAccount       = "platform:liability"
	PlatformDiamondLiabilityAccount = "platform:diamond_liability"
)

func UserCoinsAccount(accountID string) string    { return "user:" + accountID + ":coins" }
func UserDiamondsAccount(accountID string) string { return "user:" + accountID + ":diamonds" }

var (
	ErrUnbalancedTransaction = errors.New("ledger: entries do not sum to zero per currency")
	ErrTransactionEmpty      = errors.New("ledger: transaction has no entries")
	// ErrTransactionNotFound is returned by GetTransactionByID and
	// ReverseTransaction when the referenced transaction doesn't exist.
	ErrTransactionNotFound = errors.New("ledger: transaction not found")
	// ErrAlreadyReversed guards against reversing the same transaction
	// twice under two different idempotency keys — a transaction can be
	// refunded exactly once (doc rule 15: "create a reversal entry", not
	// "repeatedly"). A retry under the *same* idempotency key still
	// short-circuits to the original reversal, same as PostTransaction.
	ErrAlreadyReversed = errors.New("ledger: this transaction has already been reversed")
	// ErrCannotReverseAReversal blocks reversing a reversal — refunding a
	// refund makes no financial sense and would just recreate the original
	// (already-refunded) charge.
	ErrCannotReverseAReversal = errors.New("ledger: cannot reverse a reversal transaction")
)

// InsufficientBalanceError carries the exact shortfall so the client can
// pre-fill a top-up sheet (doc 11 §4, step 7b) instead of a bare rejection.
type InsufficientBalanceError struct {
	AccountID string
	Currency  Currency
	Required  int64
	Available int64
}

func (e *InsufficientBalanceError) Error() string {
	return "ledger: insufficient balance"
}
func (e *InsufficientBalanceError) Shortfall() int64 { return e.Required - e.Available }

// Entry is one leg of a transaction. Amount is signed: negative is a debit,
// positive is a credit.
type Entry struct {
	AccountID string
	Amount    int64
	Currency  Currency
}

// Transaction is an immutable, already-posted ledger transaction.
type Transaction struct {
	ID             string
	Kind           string
	IdempotencyKey string
	Metadata       map[string]any
	Entries        []Entry
	CreatedAt      time.Time
}

// Repo is the ledger's storage contract. Entries are append-only —
// there is deliberately no update/delete method.
type Repo interface {
	// Balance returns the current balance for accountID in currency (0 if
	// the account has never been touched).
	Balance(ctx context.Context, accountID string, currency Currency) (int64, error)

	// PostTransaction posts entries as one atomic transaction, idempotent on
	// idempotencyKey: a retry with the same key returns the original
	// transaction and isNew=false rather than posting again (doc 11 §4's
	// "the gift is sent exactly once"). entries must sum to zero per
	// currency (ErrUnbalancedTransaction) and must not drive any account
	// negative (*InsufficientBalanceError) — on either failure, nothing is
	// applied.
	PostTransaction(ctx context.Context, kind, idempotencyKey string, metadata map[string]any, entries []Entry) (tx *Transaction, isNew bool, err error)

	// GetTransaction looks up a posted transaction by idempotency key.
	GetTransaction(ctx context.Context, idempotencyKey string) (*Transaction, error)

	// GetTransactionByID looks up a posted transaction by its transaction
	// ID (as opposed to GetTransaction, which looks up by idempotency key)
	// — returns ErrTransactionNotFound if it doesn't exist. Used by the
	// admin refund flow to find the transaction to reverse.
	GetTransactionByID(ctx context.Context, id string) (*Transaction, error)

	// ReverseTransaction posts the exact inverse of originalTxID's entries
	// as a new "reversal" transaction — never deletes or edits the
	// original (doc rule 15: "never destroy financial history"). Same
	// idempotency contract as PostTransaction: a retry with the same
	// idempotencyKey returns the original reversal, isNew=false. Fails
	// with ErrTransactionNotFound if originalTxID doesn't exist,
	// ErrAlreadyReversed if it's already been reversed once,
	// ErrCannotReverseAReversal if originalTxID is itself a reversal, or
	// *InsufficientBalanceError if reversing would drive any account
	// negative (e.g. the coins were already spent elsewhere) — in every
	// failure case nothing is applied.
	ReverseTransaction(ctx context.Context, originalTxID, idempotencyKey, reason string) (tx *Transaction, isNew bool, err error)

	// History returns entries touching accountID, most recent first —
	// the user-visible transaction ledger (doc 12 Phase 9, BT-07).
	History(ctx context.Context, accountID, cursor string, limit int) ([]HistoryItem, string, error)
}

// HistoryItem is one ledger entry from one account's point of view.
type HistoryItem struct {
	TransactionID string         `json:"transaction_id"`
	Kind          string         `json:"kind"`
	Amount        int64          `json:"amount"`
	Currency      Currency       `json:"currency"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}
