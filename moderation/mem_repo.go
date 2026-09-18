package moderation

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// ─── Reports ────────────────────────────────────────────────────────────────

type MemReportRepo struct {
	mu   sync.Mutex
	seq  int
	byID map[string]*Report
	// lastReportAt tracks the most recent report from (reporterID, subjectID)
	// for the 24h rate limit.
	lastReportAt map[[2]string]time.Time
}

func NewMemReportRepo() *MemReportRepo {
	return &MemReportRepo{
		byID:         make(map[string]*Report),
		lastReportAt: make(map[[2]string]time.Time),
	}
}

func (r *MemReportRepo) RecentlyReported(_ context.Context, reporterID, subjectID string, within time.Duration) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.lastReportAt[[2]string{reporterID, subjectID}]
	if !ok {
		return false, nil
	}
	return time.Since(last) < within, nil
}

func (r *MemReportRepo) Create(_ context.Context, rep Report) (*Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	rep.ID = fmt.Sprintf("report-%04d", r.seq)
	rep.CaseID = fmt.Sprintf("RPT-%04d", r.seq)
	r.byID[rep.ID] = &rep
	r.lastReportAt[[2]string{rep.ReporterID, rep.SubjectID}] = rep.CreatedAt
	cp := rep
	return &cp, nil
}

func (r *MemReportRepo) Get(_ context.Context, id string) (*Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep, ok := r.byID[id]
	if !ok {
		return nil, ErrReportNotFound
	}
	cp := *rep
	return &cp, nil
}

func (r *MemReportRepo) UpdateState(_ context.Context, id string, state ReportState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rep, ok := r.byID[id]
	if !ok {
		return ErrReportNotFound
	}
	rep.State = state
	return nil
}

func (r *MemReportRepo) List(_ context.Context, stateFilter ReportState) ([]Report, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Report, 0, len(r.byID))
	for _, rep := range r.byID {
		if stateFilter != "" && rep.State != stateFilter {
			continue
		}
		out = append(out, *rep)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ─── Enforcements ─────────────────────────────────────────────────────────

type MemEnforcementRepo struct {
	mu     sync.Mutex
	seq    int
	byID   map[string]*Enforcement
	byAcct map[string][]string
}

func NewMemEnforcementRepo() *MemEnforcementRepo {
	return &MemEnforcementRepo{
		byID:   make(map[string]*Enforcement),
		byAcct: make(map[string][]string),
	}
}

func (r *MemEnforcementRepo) Create(_ context.Context, e Enforcement) (*Enforcement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.ID = fmt.Sprintf("enf-%04d", r.seq)
	e.CaseID = fmt.Sprintf("ENF-%04d", r.seq)
	r.byID[e.ID] = &e
	r.byAcct[e.AccountID] = append(r.byAcct[e.AccountID], e.ID)
	cp := e
	return &cp, nil
}

func (r *MemEnforcementRepo) Get(_ context.Context, id string) (*Enforcement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok {
		return nil, ErrEnforcementNotFound
	}
	cp := *e
	return &cp, nil
}

func (r *MemEnforcementRepo) ListByAccount(_ context.Context, accountID string) ([]Enforcement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := r.byAcct[accountID]
	out := make([]Enforcement, 0, len(ids))
	for _, id := range ids {
		out = append(out, *r.byID[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ─── Appeals ──────────────────────────────────────────────────────────────

type MemAppealRepo struct {
	mu           sync.Mutex
	seq          int
	byID         map[string]*Appeal
	byEnforcement map[string]string // enforcementID -> appealID (one pending appeal at a time)
}

func NewMemAppealRepo() *MemAppealRepo {
	return &MemAppealRepo{
		byID:          make(map[string]*Appeal),
		byEnforcement: make(map[string]string),
	}
}

func (r *MemAppealRepo) HasPending(_ context.Context, enforcementID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byEnforcement[enforcementID]
	if !ok {
		return false, nil
	}
	return r.byID[id].State == AppealPending, nil
}

func (r *MemAppealRepo) Create(_ context.Context, a Appeal) (*Appeal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	a.ID = fmt.Sprintf("appeal-%04d", r.seq)
	a.CaseID = fmt.Sprintf("APL-%04d", r.seq)
	r.byID[a.ID] = &a
	r.byEnforcement[a.EnforcementID] = a.ID
	cp := a
	return &cp, nil
}

func (r *MemAppealRepo) Get(_ context.Context, id string) (*Appeal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok {
		return nil, ErrAppealNotFound
	}
	cp := *a
	return &cp, nil
}

// Decide only applies when the appeal is still Pending — the check and the
// write happen under the same lock, so of two concurrent Decide calls on
// the same appeal, exactly one succeeds and the other gets
// ErrAppealAlreadyDecided rather than silently overwriting the first
// decision (and whatever external side effect, like reinstatement, the
// caller fired based on it).
func (r *MemAppealRepo) Decide(_ context.Context, id string, state AppealState, reviewedBy string) (*Appeal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.byID[id]
	if !ok {
		return nil, ErrAppealNotFound
	}
	if a.State != AppealPending {
		return nil, ErrAppealAlreadyDecided
	}
	now := time.Now()
	a.State = state
	a.ReviewedBy = reviewedBy
	a.DecidedAt = &now
	cp := *a
	return &cp, nil
}

func (r *MemAppealRepo) List(_ context.Context, stateFilter AppealState) ([]Appeal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Appeal, 0, len(r.byID))
	for _, a := range r.byID {
		if stateFilter != "" && a.State != stateFilter {
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ─── Risk profiles ────────────────────────────────────────────────────────

type MemRiskRepo struct {
	mu       sync.Mutex
	profiles map[string]*RiskProfile
}

func NewMemRiskRepo() *MemRiskRepo {
	return &MemRiskRepo{profiles: make(map[string]*RiskProfile)}
}

func (r *MemRiskRepo) Get(_ context.Context, accountID string) (*RiskProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.profiles[accountID]
	if !ok {
		return &RiskProfile{AccountID: accountID, ReviewStatus: ReviewNone, RiskReasons: []string{}}, nil
	}
	cp := *p
	cp.RiskReasons = append([]string{}, p.RiskReasons...)
	return &cp, nil
}

func (r *MemRiskRepo) Upsert(_ context.Context, p RiskProfile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := p
	r.profiles[p.AccountID] = &cp
	return nil
}

func (r *MemRiskRepo) ListFlagged(_ context.Context) ([]RiskProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RiskProfile, 0)
	for _, p := range r.profiles {
		if p.ReviewStatus != ReviewNone {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RiskScore > out[j].RiskScore })
	return out, nil
}

// ─── Moderator role set ──────────────────────────────────────────────────
//
// There is no RBAC/admin system yet (that's roadmap Phase 16) — this is a
// minimal in-memory allowlist granted only through the dev-only escalation
// (see modsvc.Service.DevGrantModerator's doc comment), same category of
// gap as age assurance and KYC. NEVER ships to production.

type MemModeratorRepo struct {
	mu  sync.Mutex
	set map[string]bool
}

func NewMemModeratorRepo() *MemModeratorRepo {
	return &MemModeratorRepo{set: make(map[string]bool)}
}

func (r *MemModeratorRepo) IsModerator(_ context.Context, accountID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.set[accountID], nil
}

func (r *MemModeratorRepo) Grant(_ context.Context, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set[accountID] = true
	return nil
}
