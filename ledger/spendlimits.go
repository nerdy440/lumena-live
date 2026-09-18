package ledger

import (
	"context"
	"errors"
	"sync"
	"time"
)

// SpendLimits are self-set purchase caps (doc 11 §9): "lowering them is
// immediate; raising them requires a 48-hour cooling-off period." Caps are
// coins-per-purchase-value in the SKU's price_minor terms — simplified
// here to a coin cap, since this dev build has no multi-currency pricing.
type SpendLimits struct {
	AccountID  string `json:"account_id"`
	DailyCap   int64  `json:"daily_cap"` // 0 = no cap
	WeeklyCap  int64  `json:"weekly_cap"`
	MonthlyCap int64  `json:"monthly_cap"`

	// CoolingOff, once activated, blocks all coin purchases until
	// CoolingOffUntil (doc 11 §9: "can be activated by the user at any
	// time; during cooling-off, coin purchases are blocked").
	CoolingOff      bool      `json:"cooling_off"`
	CoolingOffUntil time.Time `json:"cooling_off_until"`

	// A raised cap does not take effect until PendingCapEffectiveAt — the
	// 48h delay. A lowered cap applies immediately and clears any pending
	// raise (doc 11 §9).
	PendingDailyCap    int64     `json:"pending_daily_cap"`
	PendingWeeklyCap   int64     `json:"pending_weekly_cap"`
	PendingMonthlyCap  int64     `json:"pending_monthly_cap"`
	PendingEffectiveAt time.Time `json:"pending_effective_at"`
}

var (
	ErrCoolingOff        = errors.New("ledger: purchases are blocked during the user's cooling-off period")
	ErrSpendLimitExceeded = errors.New("ledger: this purchase would exceed a self-set spend limit")
)

const raiseDelay = 48 * time.Hour

// SpendLimitsRepo stores one SpendLimits row per account.
type SpendLimitsRepo interface {
	Get(ctx context.Context, accountID string) (*SpendLimits, error)
	Set(ctx context.Context, limits *SpendLimits) error
}

type MemSpendLimitsRepo struct {
	mu   sync.Mutex
	byID map[string]*SpendLimits
}

func NewMemSpendLimitsRepo() *MemSpendLimitsRepo {
	return &MemSpendLimitsRepo{byID: make(map[string]*SpendLimits)}
}

var _ SpendLimitsRepo = (*MemSpendLimitsRepo)(nil)

func (r *MemSpendLimitsRepo) Get(_ context.Context, accountID string) (*SpendLimits, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.byID[accountID]; ok {
		cp := *l
		return &cp, nil
	}
	return &SpendLimits{AccountID: accountID}, nil
}

func (r *MemSpendLimitsRepo) Set(_ context.Context, limits *SpendLimits) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *limits
	r.byID[limits.AccountID] = &cp
	return nil
}

// ApplyCapChange updates daily/weekly/monthly caps following the
// lower-is-immediate / raise-needs-48h rule, resolving any pending raise
// whose delay has already elapsed. now is injected for testability.
func ApplyCapChange(current *SpendLimits, newDaily, newWeekly, newMonthly int64, now time.Time) *SpendLimits {
	next := *current
	resolvePendingIfDue(&next, now)

	next.DailyCap, next.PendingDailyCap = applyOneCap(next.DailyCap, next.PendingDailyCap, newDaily)
	next.WeeklyCap, next.PendingWeeklyCap = applyOneCap(next.WeeklyCap, next.PendingWeeklyCap, newWeekly)
	next.MonthlyCap, next.PendingMonthlyCap = applyOneCap(next.MonthlyCap, next.PendingMonthlyCap, newMonthly)

	if next.PendingDailyCap != 0 || next.PendingWeeklyCap != 0 || next.PendingMonthlyCap != 0 {
		next.PendingEffectiveAt = now.Add(raiseDelay)
	} else {
		next.PendingEffectiveAt = time.Time{}
	}
	return &next
}

// applyOneCap: 0 means "no change requested" for that field. A cap of 0
// requested from a non-zero cap is not supported by this simplified
// endpoint (removing a cap entirely) — callers pass the existing value to
// leave it untouched.
func applyOneCap(current, pending, requested int64) (newCurrent, newPending int64) {
	if requested == 0 || requested == current {
		return current, pending
	}
	if current == 0 || requested < current {
		return requested, 0 // lowering (or setting from unset) is immediate
	}
	return current, requested // raising is deferred
}

// resolvePendingIfDue promotes a pending raise into effect once its delay
// has elapsed — called lazily on read/write rather than by a background
// job, since the caps themselves are read on every purchase check anyway.
func resolvePendingIfDue(l *SpendLimits, now time.Time) {
	if l.PendingEffectiveAt.IsZero() || now.Before(l.PendingEffectiveAt) {
		return
	}
	if l.PendingDailyCap != 0 {
		l.DailyCap = l.PendingDailyCap
		l.PendingDailyCap = 0
	}
	if l.PendingWeeklyCap != 0 {
		l.WeeklyCap = l.PendingWeeklyCap
		l.PendingWeeklyCap = 0
	}
	if l.PendingMonthlyCap != 0 {
		l.MonthlyCap = l.PendingMonthlyCap
		l.PendingMonthlyCap = 0
	}
	l.PendingEffectiveAt = time.Time{}
}

// ResolvePending is the exported form used by callers that read limits
// without going through ApplyCapChange.
func ResolvePending(l *SpendLimits, now time.Time) *SpendLimits {
	cp := *l
	resolvePendingIfDue(&cp, now)
	return &cp
}
