package creator

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemKYCRepo stores one KYC record per account.
type MemKYCRepo struct {
	mu      sync.RWMutex
	records map[string]*KYCRecord
}

func NewMemKYCRepo() *MemKYCRepo {
	return &MemKYCRepo{records: make(map[string]*KYCRecord)}
}

func (r *MemKYCRepo) Get(_ context.Context, accountID string) (*KYCRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.records[accountID]
	if !ok {
		return &KYCRecord{AccountID: accountID, Status: KYCNone}, nil
	}
	cp := *rec
	return &cp, nil
}

func (r *MemKYCRepo) Submit(_ context.Context, accountID, legalName, country string) (*KYCRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := &KYCRecord{
		AccountID:   accountID,
		Status:      KYCPending,
		LegalName:   legalName,
		Country:     country,
		SubmittedAt: time.Now(),
	}
	r.records[accountID] = rec
	cp := *rec
	return &cp, nil
}

// Decide sets the KYC record to verified or rejected — used only by the
// dev-only escalation (see creatorsvc.Service.DevApproveKYC's doc comment);
// a real deployment wires this to a vendor webhook instead. Creates the
// record if the account never called Submit, mirroring authsvc's
// DevAssureAge, which likewise works without a prior DeclareAge.
func (r *MemKYCRepo) Decide(_ context.Context, accountID string, status KYCStatus) (*KYCRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.records[accountID]
	if !ok {
		rec = &KYCRecord{AccountID: accountID, SubmittedAt: time.Now()}
		r.records[accountID] = rec
	}
	now := time.Now()
	rec.Status = status
	rec.DecidedAt = &now
	cp := *rec
	return &cp, nil
}

// MemPayoutRepo stores payout requests, append-only aside from status updates.
type MemPayoutRepo struct {
	mu      sync.Mutex
	seq     int
	byID    map[string]*Payout
	byAcct  map[string][]string // accountID -> payout IDs, insertion order
}

func NewMemPayoutRepo() *MemPayoutRepo {
	return &MemPayoutRepo{
		byID:   make(map[string]*Payout),
		byAcct: make(map[string][]string),
	}
}

func (r *MemPayoutRepo) Create(_ context.Context, p Payout) (*Payout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	p.ID = fmt.Sprintf("payout-%04d", r.seq)
	r.byID[p.ID] = &p
	r.byAcct[p.AccountID] = append(r.byAcct[p.AccountID], p.ID)
	cp := p
	return &cp, nil
}

func (r *MemPayoutRepo) UpdateStatus(_ context.Context, id string, status PayoutStatus, failureReason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byID[id]
	if !ok {
		return ErrPayoutNotFound
	}
	p.Status = status
	p.FailureReason = failureReason
	p.UpdatedAt = time.Now()
	return nil
}

// Update applies mutate to the stored payout under the repo's lock — the
// in-memory stand-in for a row-locked `SELECT ... FOR UPDATE` + `UPDATE`
// (doc rule 13: balance/status-changing operations must be atomic). Every
// admin transition (approve/reject/processing/paid/failed) goes through
// this single choke point rather than ad-hoc field writers.
func (r *MemPayoutRepo) Update(_ context.Context, id string, mutate func(*Payout) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byID[id]
	if !ok {
		return ErrPayoutNotFound
	}
	return mutate(p)
}

func (r *MemPayoutRepo) Get(_ context.Context, id string) (*Payout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, ErrPayoutNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *MemPayoutRepo) ListByAccount(_ context.Context, accountID string) ([]Payout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.byAcct[accountID]
	out := make([]Payout, 0, len(ids))
	for _, id := range ids {
		out = append(out, *r.byID[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.After(out[j].RequestedAt) })
	return out, nil
}

// PendingOrProcessingTotal sums the amount of this account's payouts that
// have been requested but not yet completed/failed — money already
// committed to a payout in flight, so it can't be double-spent by a second
// concurrent request.
func (r *MemPayoutRepo) PendingOrProcessingTotal(_ context.Context, accountID string) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, id := range r.byAcct[accountID] {
		p := r.byID[id]
		if isReserved(p.Status) {
			total += p.AmountDiamonds
		}
	}
	return total, nil
}

// isReserved reports whether a payout's amount is still held out of the
// creator's available balance — true from the moment it's requested until
// it's either paid or its hold is reversed (rejected/failed).
func isReserved(s PayoutStatus) bool {
	switch s {
	case PayoutRequested, PayoutApproved, PayoutProcessing:
		return true
	default:
		return false
	}
}

// CountPending returns how many payouts across all accounts are currently
// awaiting admin action or execution — not part of any Service interface,
// used only by the admin module's platform health dashboard (Phase 16).
func (r *MemPayoutRepo) CountPending(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, p := range r.byID {
		if isReserved(p.Status) {
			count++
		}
	}
	return count, nil
}

// ListAll returns every payout across all accounts, optionally filtered to
// one status — the admin withdrawal-management queue (doc rule 20). Newest
// first, same ordering convention as ListByAccount.
func (r *MemPayoutRepo) ListAll(_ context.Context, statusFilter PayoutStatus) ([]Payout, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Payout, 0, len(r.byID))
	for _, p := range r.byID {
		if statusFilter != "" && p.Status != statusFilter {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt.After(out[j].RequestedAt) })
	return out, nil
}

// ─── Payout profile ─────────────────────────────────────────────────────────

// MemPayoutProfileRepo stores one payout profile per creator account.
type MemPayoutProfileRepo struct {
	mu       sync.RWMutex
	profiles map[string]*PayoutProfile
}

func NewMemPayoutProfileRepo() *MemPayoutProfileRepo {
	return &MemPayoutProfileRepo{profiles: make(map[string]*PayoutProfile)}
}

// Get returns the stored profile, or a zero-value profile (empty Method) if
// the creator has never set one — callers check Method == "" to detect
// "not set yet" rather than treating that as an error.
func (r *MemPayoutProfileRepo) Get(_ context.Context, accountID string) (*PayoutProfile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if p, ok := r.profiles[accountID]; ok {
		cp := *p
		return &cp, nil
	}
	return &PayoutProfile{AccountID: accountID}, nil
}

func (r *MemPayoutProfileRepo) Set(_ context.Context, p PayoutProfile) (*PayoutProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p.UpdatedAt = time.Now()
	cp := p
	r.profiles[p.AccountID] = &cp
	out := cp
	return &out, nil
}
