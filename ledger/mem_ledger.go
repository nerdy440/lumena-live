package ledger

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// MemLedger is the in-memory dev/test Repo implementation, following the
// same pattern as every other module in this workspace (auth/memrepo,
// profile's MemProfileRepo, chat's MemChatRepo): production would swap this
// for the doc 06 §9 Postgres schema (SERIALIZABLE transactions, FOR UPDATE
// row locks) behind the same interface.
type MemLedger struct {
	mu sync.Mutex

	balances   map[balanceKey]int64
	byKey      map[string]*Transaction // idempotency key -> transaction
	byID       map[string]*Transaction
	byAcct     map[string][]HistoryItem // account id -> entries touching it, oldest first
	reversalOf map[string]string        // original transaction id -> its reversal transaction id
	seq        int
}

type balanceKey struct {
	accountID string
	currency  Currency
}

func NewMemLedger() *MemLedger {
	return &MemLedger{
		balances:   make(map[balanceKey]int64),
		byKey:      make(map[string]*Transaction),
		byID:       make(map[string]*Transaction),
		byAcct:     make(map[string][]HistoryItem),
		reversalOf: make(map[string]string),
	}
}

var _ Repo = (*MemLedger)(nil)

// balanceEntry is balanceKey/balance flattened to a JSON-friendly shape —
// encoding/json can't marshal a map whose key is a struct.
type balanceEntry struct {
	AccountID string   `json:"account_id"`
	Currency  Currency `json:"currency"`
	Balance   int64    `json:"balance"`
}

type ledgerSnapshot struct {
	Balances   []balanceEntry          `json:"balances"`
	ByID       map[string]*Transaction `json:"by_id"`
	ByAcct     map[string][]HistoryItem `json:"by_acct"`
	ReversalOf map[string]string       `json:"reversal_of"`
	Seq        int                     `json:"seq"`
}

// Snapshot serializes the whole ledger for local-persistence purposes (see
// feed/cmd/api's periodic snapshot-to-disk — a lightweight stand-in for a
// real database in this dev build, not a substitute for one in production).
// byKey (idempotency-key -> transaction) is deliberately NOT persisted: a
// client that was mid-retry across a restart will simply post a fresh
// transaction, no different from any other first attempt, so it doesn't
// need durability the way account balances and history do.
func (l *MemLedger) Snapshot() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := ledgerSnapshot{
		Balances:   make([]balanceEntry, 0, len(l.balances)),
		ByID:       l.byID,
		ByAcct:     l.byAcct,
		ReversalOf: l.reversalOf,
		Seq:        l.seq,
	}
	for k, v := range l.balances {
		s.Balances = append(s.Balances, balanceEntry{AccountID: k.accountID, Currency: k.currency, Balance: v})
	}
	return json.Marshal(s)
}

// Restore replaces this ledger's entire state from a Snapshot's output.
// Only meaningful immediately after NewMemLedger, before any traffic —
// callers must not mix Restore with concurrent writes.
func (l *MemLedger) Restore(data []byte) error {
	var s ledgerSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.balances = make(map[balanceKey]int64, len(s.Balances))
	for _, e := range s.Balances {
		l.balances[balanceKey{accountID: e.AccountID, currency: e.Currency}] = e.Balance
	}
	if s.ByID != nil {
		l.byID = s.ByID
	}
	if s.ByAcct != nil {
		l.byAcct = s.ByAcct
	}
	if s.ReversalOf != nil {
		l.reversalOf = s.ReversalOf
	}
	l.seq = s.Seq
	return nil
}

// IntegrityCheck sums every account's balance per currency and asserts it
// is zero (doc 06 §9 invariant 2: "the sum per currency across all accounts
// in the system is always zero... checked nightly across the whole
// ledger"). Since every posted transaction already balances to zero
// in isolation, a nonzero sum here can only mean a bug bypassed
// PostTransaction — this is the whole-ledger cross-check, not a
// per-transaction one.
func (l *MemLedger) IntegrityCheck() map[Currency]int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	sums := make(map[Currency]int64)
	for k, v := range l.balances {
		sums[k.currency] += v
	}
	return sums
}

func (l *MemLedger) Balance(_ context.Context, accountID string, currency Currency) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.balances[balanceKey{accountID, currency}], nil
}

func (l *MemLedger) PostTransaction(_ context.Context, kind, idempotencyKey string, metadata map[string]any, entries []Entry) (*Transaction, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.postLocked(kind, idempotencyKey, metadata, entries)
}

// postLocked is PostTransaction's body, factored out so ReverseTransaction
// can post its compensating entries under the same lock acquisition rather
// than recursively locking l.mu.
func (l *MemLedger) postLocked(kind, idempotencyKey string, metadata map[string]any, entries []Entry) (*Transaction, bool, error) {
	if existing, ok := l.byKey[idempotencyKey]; ok {
		return existing, false, nil
	}
	if len(entries) == 0 {
		return nil, false, ErrTransactionEmpty
	}

	sums := make(map[Currency]int64)
	for _, e := range entries {
		sums[e.Currency] += e.Amount
	}
	for _, sum := range sums {
		if sum != 0 {
			return nil, false, ErrUnbalancedTransaction
		}
	}

	// Validate before applying anything — a transaction either posts
	// entirely or not at all. proposed tracks the running balance per
	// account across this transaction's own entries (in case more than one
	// entry touches the same account), while the reported shortfall is
	// always relative to the account's actual pre-transaction balance.
	proposed := make(map[balanceKey]int64, len(entries))
	for _, e := range entries {
		k := balanceKey{e.AccountID, e.Currency}
		base, ok := proposed[k]
		if !ok {
			base = l.balances[k]
		}
		next := base + e.Amount
		if next < 0 && !isPlatformAccount(e.AccountID) {
			return nil, false, &InsufficientBalanceError{
				AccountID: e.AccountID, Currency: e.Currency,
				Required:  -e.Amount,
				Available: l.balances[k],
			}
		}
		proposed[k] = next
	}

	for k, v := range proposed {
		l.balances[k] = v
	}

	l.seq++
	tx := &Transaction{
		ID:             "tx-" + itoaLedger(l.seq),
		Kind:           kind,
		IdempotencyKey: idempotencyKey,
		Metadata:       metadata,
		Entries:        append([]Entry(nil), entries...),
		CreatedAt:      time.Now(),
	}
	l.byKey[idempotencyKey] = tx
	l.byID[tx.ID] = tx
	for _, e := range entries {
		l.byAcct[e.AccountID] = append(l.byAcct[e.AccountID], HistoryItem{
			TransactionID: tx.ID, Kind: kind, Amount: e.Amount, Currency: e.Currency,
			Metadata: metadata, CreatedAt: tx.CreatedAt,
		})
	}
	return tx, true, nil
}

// ListByKind returns every posted transaction of the given kind at or after
// since, oldest first. Not part of the Repo interface — read-only, used
// only by the admin module's economy oversight (Phase 16) to scan for gift
// loops. Deliberately has no write counterpart: doc 12's exit gate "admin
// cannot edit ledger entries directly" is enforced by this being the only
// bulk-read surface the admin module is ever wired to, never a mutator.
func (l *MemLedger) ListByKind(_ context.Context, kind string, since time.Time) ([]Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Transaction
	for _, tx := range l.byID {
		if tx.Kind == kind && !tx.CreatedAt.Before(since) {
			out = append(out, *tx)
		}
	}
	return out, nil
}

func (l *MemLedger) GetTransaction(_ context.Context, idempotencyKey string) (*Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byKey[idempotencyKey], nil
}

func (l *MemLedger) GetTransactionByID(_ context.Context, id string) (*Transaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tx, ok := l.byID[id]
	if !ok {
		return nil, ErrTransactionNotFound
	}
	cp := *tx
	return &cp, nil
}

func (l *MemLedger) ReverseTransaction(_ context.Context, originalTxID, idempotencyKey, reason string) (*Transaction, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if existing, ok := l.byKey[idempotencyKey]; ok {
		return existing, false, nil
	}
	original, ok := l.byID[originalTxID]
	if !ok {
		return nil, false, ErrTransactionNotFound
	}
	if original.Kind == "reversal" {
		return nil, false, ErrCannotReverseAReversal
	}
	if _, already := l.reversalOf[originalTxID]; already {
		return nil, false, ErrAlreadyReversed
	}

	entries := make([]Entry, len(original.Entries))
	for i, e := range original.Entries {
		entries[i] = Entry{AccountID: e.AccountID, Amount: -e.Amount, Currency: e.Currency}
	}
	metadata := map[string]any{
		"reversed_transaction_id": originalTxID,
		"reason":                  reason,
		"original_kind":           original.Kind,
	}

	tx, isNew, err := l.postLocked("reversal", idempotencyKey, metadata, entries)
	if err != nil {
		return nil, false, err
	}
	if isNew {
		l.reversalOf[originalTxID] = tx.ID
	}
	return tx, isNew, nil
}

func (l *MemLedger) History(_ context.Context, accountID, cursor string, limit int) ([]HistoryItem, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	all := l.byAcct[accountID]
	// Most recent first.
	rev := make([]HistoryItem, len(all))
	for i, h := range all {
		rev[len(all)-1-i] = h
	}

	start := 0
	if cursor != "" {
		if n, ok := parseUintLedger(cursor); ok {
			start = int(n)
		}
	}
	if start > len(rev) {
		start = len(rev)
	}
	rest := rev[start:]

	var out []HistoryItem
	for _, h := range rest {
		if len(out) >= limit {
			break
		}
		out = append(out, h)
	}
	var nextCursor string
	if start+len(out) < len(rev) {
		nextCursor = itoaLedger(start + len(out))
	}
	return out, nextCursor, nil
}

// isPlatformAccount reports whether accountID is an internal control account
// (platform:revenue, platform:liability, platform:diamond_liability) rather
// than a real user wallet. The never-negative invariant (doc 06 §9's
// `CHECK (balance >= 0)`) protects spendable wallets; platform control
// accounts are accounting constructs that legitimately float negative —
// e.g. platform:diamond_liability goes more negative as more diamonds are
// issued, backing them the way platform:liability backs issued coins.
func isPlatformAccount(accountID string) bool {
	return strings.HasPrefix(accountID, "platform:")
}

func itoaLedger(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func parseUintLedger(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
	}
	return n, true
}
