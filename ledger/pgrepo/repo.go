// Package pgrepo is the Postgres-backed implementation of ledger.Repo —
// the real-database counterpart to ledger.MemLedger. See
// migrations/0002_ledger.sql for the schema and mem_ledger.go's own doc
// comments for the invariants this must preserve: idempotent posts,
// entries balanced to zero per currency, spendable balances never go
// negative (platform:* control accounts are exempt — they legitimately
// float negative as the liability side of issued coins/diamonds).
//
// PostTransaction runs at SERIALIZABLE isolation with a bounded retry on
// serialization conflicts (SQLSTATE 40001) — the real-database analogue
// of mem_ledger.go's single mutex: two concurrent posts touching the same
// account can't both read a stale balance and both succeed.
package pgrepo

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lumena/ledger"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ ledger.Repo = (*Repo)(nil)

func newTxID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return fmt.Sprintf("tx-%x", b)
}

func isPlatformAccount(accountID string) bool {
	return strings.HasPrefix(accountID, "platform:")
}

func (r *Repo) Balance(ctx context.Context, accountID string, currency ledger.Currency) (int64, error) {
	var sum int64
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(amount), 0) FROM ledger_entries WHERE account_id = $1 AND currency = $2`,
		accountID, string(currency)).Scan(&sum)
	return sum, err
}

const maxSerializationRetries = 5

func (r *Repo) PostTransaction(ctx context.Context, kind, idempotencyKey string, metadata map[string]any, entries []ledger.Entry) (*ledger.Transaction, bool, error) {
	if len(entries) == 0 {
		return nil, false, ledger.ErrTransactionEmpty
	}
	sums := make(map[ledger.Currency]int64)
	for _, e := range entries {
		sums[e.Currency] += e.Amount
	}
	for _, sum := range sums {
		if sum != 0 {
			return nil, false, ledger.ErrUnbalancedTransaction
		}
	}

	for attempt := 0; attempt < maxSerializationRetries; attempt++ {
		tx, isNew, err := r.postOnce(ctx, kind, idempotencyKey, metadata, entries)
		if err == nil {
			return tx, isNew, nil
		}
		if isSerializationFailure(err) {
			continue // real conflict with a concurrent post — safe to retry from scratch
		}
		return nil, false, err
	}
	return nil, false, fmt.Errorf("ledger/pgrepo: PostTransaction: too many serialization conflicts")
}

func (r *Repo) postOnce(ctx context.Context, kind, idempotencyKey string, metadata map[string]any, entries []ledger.Entry) (*ledger.Transaction, bool, error) {
	dbTx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, false, err
	}
	defer dbTx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if existing, err := getTransactionTx(ctx, dbTx, "idempotency_key", idempotencyKey); err != nil {
		return nil, false, err
	} else if existing != nil {
		return existing, false, nil
	}

	// Validate the resulting balance for every distinct account+currency
	// this transaction touches, using the running total across this
	// transaction's OWN entries (mirrors mem_ledger.go's `proposed` map)
	// so an account touched twice in one call is checked against its
	// final post-transaction balance, not an intermediate one.
	type key struct {
		account  string
		currency ledger.Currency
	}
	proposed := make(map[key]int64)
	seenBase := make(map[key]bool)
	for _, e := range entries {
		k := key{e.AccountID, e.Currency}
		if !seenBase[k] {
			var base int64
			if err := dbTx.QueryRow(ctx,
				`SELECT COALESCE(SUM(amount), 0) FROM ledger_entries WHERE account_id = $1 AND currency = $2`,
				e.AccountID, string(e.Currency)).Scan(&base); err != nil {
				return nil, false, err
			}
			proposed[k] = base
			seenBase[k] = true
		}
		proposed[k] += e.Amount
		if proposed[k] < 0 && !isPlatformAccount(e.AccountID) {
			available := proposed[k] - e.Amount
			return nil, false, &ledger.InsufficientBalanceError{
				AccountID: e.AccountID, Currency: e.Currency,
				Required:  -e.Amount,
				Available: available,
			}
		}
	}

	id := newTxID()
	now := time.Now()
	if _, err := dbTx.Exec(ctx,
		`INSERT INTO ledger_transactions (id, kind, idempotency_key, metadata, created_at) VALUES ($1, $2, $3, $4, $5)`,
		id, kind, idempotencyKey, metadataJSON(metadata), now); err != nil {
		return nil, false, err
	}
	for _, e := range entries {
		if _, err := dbTx.Exec(ctx,
			`INSERT INTO ledger_entries (transaction_id, account_id, amount, currency, created_at) VALUES ($1, $2, $3, $4, $5)`,
			id, e.AccountID, e.Amount, string(e.Currency), now); err != nil {
			return nil, false, err
		}
	}
	if err := dbTx.Commit(ctx); err != nil {
		return nil, false, err
	}

	return &ledger.Transaction{
		ID: id, Kind: kind, IdempotencyKey: idempotencyKey, Metadata: metadata,
		Entries: append([]ledger.Entry(nil), entries...), CreatedAt: now,
	}, true, nil
}

func (r *Repo) GetTransaction(ctx context.Context, idempotencyKey string) (*ledger.Transaction, error) {
	return getTransactionTx(ctx, r.pool, "idempotency_key", idempotencyKey)
}

func (r *Repo) GetTransactionByID(ctx context.Context, id string) (*ledger.Transaction, error) {
	tx, err := getTransactionTx(ctx, r.pool, "id", id)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, ledger.ErrTransactionNotFound
	}
	return tx, nil
}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx so
// getTransactionTx can run either inside an open transaction (the
// idempotency pre-check in postOnce) or standalone (GetTransaction /
// GetTransactionByID).
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func getTransactionTx(ctx context.Context, q querier, col, val string) (*ledger.Transaction, error) {
	rows, err := q.Query(ctx,
		fmt.Sprintf(`SELECT t.id, t.kind, t.idempotency_key, t.metadata, t.created_at,
		                    e.account_id, e.amount, e.currency
		             FROM ledger_transactions t
		             JOIN ledger_entries e ON e.transaction_id = t.id
		             WHERE t.%s = $1
		             ORDER BY e.id`, col),
		val)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tx *ledger.Transaction
	for rows.Next() {
		var id, kind, idemKey, accountID, currency string
		var metaRaw []byte
		var amount int64
		var createdAt time.Time
		if err := rows.Scan(&id, &kind, &idemKey, &metaRaw, &createdAt, &accountID, &amount, &currency); err != nil {
			return nil, err
		}
		if tx == nil {
			tx = &ledger.Transaction{
				ID: id, Kind: kind, IdempotencyKey: idemKey, Metadata: parseMetadata(metaRaw), CreatedAt: createdAt,
			}
		}
		tx.Entries = append(tx.Entries, ledger.Entry{AccountID: accountID, Amount: amount, Currency: ledger.Currency(currency)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tx, nil
}

func (r *Repo) ReverseTransaction(ctx context.Context, originalTxID, idempotencyKey, reason string) (*ledger.Transaction, bool, error) {
	if existing, err := r.GetTransaction(ctx, idempotencyKey); err != nil {
		return nil, false, err
	} else if existing != nil {
		return existing, false, nil
	}

	original, err := r.GetTransactionByID(ctx, originalTxID)
	if err != nil {
		return nil, false, err
	}
	if original.Kind == "reversal" {
		return nil, false, ledger.ErrCannotReverseAReversal
	}
	var alreadyReversed bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ledger_reversals WHERE original_transaction_id = $1)`, originalTxID).Scan(&alreadyReversed); err != nil {
		return nil, false, err
	}
	if alreadyReversed {
		return nil, false, ledger.ErrAlreadyReversed
	}

	entries := make([]ledger.Entry, len(original.Entries))
	for i, e := range original.Entries {
		entries[i] = ledger.Entry{AccountID: e.AccountID, Amount: -e.Amount, Currency: e.Currency}
	}
	metadata := map[string]any{
		"reversed_transaction_id": originalTxID,
		"reason":                  reason,
		"original_kind":           original.Kind,
	}

	tx, isNew, err := r.PostTransaction(ctx, "reversal", idempotencyKey, metadata, entries)
	if err != nil {
		return nil, false, err
	}
	if isNew {
		if _, err := r.pool.Exec(ctx,
			`INSERT INTO ledger_reversals (original_transaction_id, reversal_transaction_id) VALUES ($1, $2)`,
			originalTxID, tx.ID); err != nil {
			return nil, false, err
		}
	}
	return tx, isNew, nil
}

func (r *Repo) History(ctx context.Context, accountID, cursor string, limit int) ([]ledger.HistoryItem, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := 0
	if cursor != "" {
		if n, err := strconv.Atoi(cursor); err == nil && n > 0 {
			offset = n
		}
	}

	rows, err := r.pool.Query(ctx,
		`SELECT t.id, t.kind, t.metadata, e.amount, e.currency, e.created_at
		 FROM ledger_entries e
		 JOIN ledger_transactions t ON t.id = e.transaction_id
		 WHERE e.account_id = $1
		 ORDER BY e.id DESC
		 OFFSET $2 LIMIT $3`,
		accountID, offset, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var items []ledger.HistoryItem
	for rows.Next() {
		var h ledger.HistoryItem
		var currency string
		var metaRaw []byte
		if err := rows.Scan(&h.TransactionID, &h.Kind, &metaRaw, &h.Amount, &currency, &h.CreatedAt); err != nil {
			return nil, "", err
		}
		h.Currency = ledger.Currency(currency)
		h.Metadata = parseMetadata(metaRaw)
		items = append(items, h)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(items) > limit {
		items = items[:limit]
		nextCursor = strconv.Itoa(offset + limit)
	}
	return items, nextCursor, nil
}

// ListByKind returns every posted transaction of the given kind at or
// after since, matching mem_ledger.go's ListByKind exactly — not part of
// the ledger.Repo interface, but relied on directly by the admin,
// analytics, and fraud modules' read-only economy scans (see that
// method's doc comment for why this is deliberately read-only).
func (r *Repo) ListByKind(ctx context.Context, kind string, since time.Time) ([]ledger.Transaction, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT t.id, t.kind, t.idempotency_key, t.metadata, t.created_at,
		       e.account_id, e.amount, e.currency
		FROM ledger_transactions t
		JOIN ledger_entries e ON e.transaction_id = t.id
		WHERE t.kind = $1 AND t.created_at >= $2
		ORDER BY t.id, e.id`, kind, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var order []string
	byID := make(map[string]*ledger.Transaction)
	for rows.Next() {
		var id, txKind, idemKey, accountID, currency string
		var metaRaw []byte
		var amount int64
		var createdAt time.Time
		if err := rows.Scan(&id, &txKind, &idemKey, &metaRaw, &createdAt, &accountID, &amount, &currency); err != nil {
			return nil, err
		}
		tx, ok := byID[id]
		if !ok {
			tx = &ledger.Transaction{ID: id, Kind: txKind, IdempotencyKey: idemKey, Metadata: parseMetadata(metaRaw), CreatedAt: createdAt}
			byID[id] = tx
			order = append(order, id)
		}
		tx.Entries = append(tx.Entries, ledger.Entry{AccountID: accountID, Amount: amount, Currency: ledger.Currency(currency)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ledger.Transaction, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// IntegrityCheck sums every entry per currency across the whole ledger —
// same invariant as mem_ledger.go's IntegrityCheck (doc 06 §9 invariant
// 2), computed with one aggregate query instead of a full balance-map
// scan since Postgres already indexes ledger_entries by currency.
func (r *Repo) IntegrityCheck() map[ledger.Currency]int64 {
	sums := make(map[ledger.Currency]int64)
	rows, err := r.pool.Query(context.Background(), `SELECT currency, SUM(amount) FROM ledger_entries GROUP BY currency`)
	if err != nil {
		return sums
	}
	defer rows.Close()
	for rows.Next() {
		var currency string
		var sum int64
		if err := rows.Scan(&currency, &sum); err != nil {
			continue
		}
		sums[ledger.Currency(currency)] = sum
	}
	return sums
}

func isSerializationFailure(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 40001")
}

func metadataJSON(m map[string]any) []byte {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}

func parseMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
