package match

import (
	"context"
	"sync"
	"time"
)

type Repo interface {
	CreateRequest(ctx context.Context, accountID string, prefs Preferences) (*Request, error)
	GetRequest(ctx context.Context, id string) (*Request, error)
	UpdateRequest(ctx context.Context, r *Request) error
	// SearchingRequests returns every request currently in "searching"
	// state, excluding excludeAccountID — the matcher's candidate pool.
	SearchingRequests(ctx context.Context, excludeAccountID string) ([]Request, error)
	ListHistory(ctx context.Context, accountID string) ([]Request, error)
	// RecentRequestCount counts requests accountID created since `since` —
	// the anti-abuse rate-limit check (MT-04).
	RecentRequestCount(ctx context.Context, accountID string, since time.Time) (int, error)

	AddCandidate(ctx context.Context, c *Candidate) error
	GetCandidate(ctx context.Context, requestID, candidateID string) (*Candidate, error)
	// GetPendingCandidate returns the currently-offered (undecided)
	// candidate for a request, if any.
	GetPendingCandidate(ctx context.Context, requestID string) (*Candidate, error)
	SetCandidateDecision(ctx context.Context, requestID, candidateID string, decision Decision) error
	// FindMirror returns the candidate row on the OTHER side of a pairing:
	// given (requestID, candidateAccountID), it finds the candidate row
	// where candidateAccountID's own request offered requestID's owner as
	// the candidate — used to check whether both sides connected.
	FindMirror(ctx context.Context, requestID, candidateAccountID string) (*Candidate, error)

	AddExclusion(ctx context.Context, e Exclusion) error
	IsExcluded(ctx context.Context, accountID, otherID string, now time.Time) (bool, error)
	// SkipCount counts how many distinct accounts have skipped
	// candidateID since `since` — feeds the repeat-offender exclusion.
	SkipCount(ctx context.Context, candidateID string, since time.Time) (int, error)
}

type MemRepo struct {
	mu         sync.Mutex
	requests   map[string]*Request
	candidates map[candidateKey]*Candidate
	exclusions map[exclusionKey]*Exclusion
	seq        int
}

type candidateKey struct{ requestID, candidateID string }
type exclusionKey struct{ accountID, excludedID string }

func NewMemRepo() *MemRepo {
	return &MemRepo{
		requests:   make(map[string]*Request),
		candidates: make(map[candidateKey]*Candidate),
		exclusions: make(map[exclusionKey]*Exclusion),
	}
}

var _ Repo = (*MemRepo)(nil)

func (r *MemRepo) CreateRequest(_ context.Context, accountID string, prefs Preferences) (*Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	req := &Request{
		ID: "match-" + itoa(r.seq), AccountID: accountID, Preferences: prefs,
		State: StateSearching, CreatedAt: time.Now(),
	}
	r.requests[req.ID] = req
	cp := *req
	return &cp, nil
}

func (r *MemRepo) GetRequest(_ context.Context, id string) (*Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	req, ok := r.requests[id]
	if !ok {
		return nil, ErrRequestNotFound
	}
	cp := *req
	return &cp, nil
}

func (r *MemRepo) UpdateRequest(_ context.Context, req *Request) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.requests[req.ID]; !ok {
		return ErrRequestNotFound
	}
	cp := *req
	r.requests[req.ID] = &cp
	return nil
}

func (r *MemRepo) SearchingRequests(_ context.Context, excludeAccountID string) ([]Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Request
	for _, req := range r.requests {
		if req.State == StateSearching && req.AccountID != excludeAccountID {
			out = append(out, *req)
		}
	}
	return out, nil
}

func (r *MemRepo) ListHistory(_ context.Context, accountID string) ([]Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Request
	for _, req := range r.requests {
		if req.AccountID == accountID {
			out = append(out, *req)
		}
	}
	return out, nil
}

func (r *MemRepo) RecentRequestCount(_ context.Context, accountID string, since time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, req := range r.requests {
		if req.AccountID == accountID && req.CreatedAt.After(since) {
			n++
		}
	}
	return n, nil
}

func (r *MemRepo) AddCandidate(_ context.Context, c *Candidate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	r.candidates[candidateKey{c.RequestID, c.CandidateID}] = &cp
	return nil
}

func (r *MemRepo) GetCandidate(_ context.Context, requestID, candidateID string) (*Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.candidates[candidateKey{requestID, candidateID}]
	if !ok {
		return nil, ErrCandidateNotFound
	}
	cp := *c
	return &cp, nil
}

func (r *MemRepo) GetPendingCandidate(_ context.Context, requestID string) (*Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var best *Candidate
	for k, c := range r.candidates {
		if k.requestID != requestID || c.Decision != "" {
			continue
		}
		if best == nil || c.OfferedAt.After(best.OfferedAt) {
			best = c
		}
	}
	if best == nil {
		return nil, nil
	}
	cp := *best
	return &cp, nil
}

func (r *MemRepo) SetCandidateDecision(_ context.Context, requestID, candidateID string, decision Decision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.candidates[candidateKey{requestID, candidateID}]
	if !ok {
		return ErrCandidateNotFound
	}
	c.Decision = decision
	return nil
}

func (r *MemRepo) FindMirror(_ context.Context, requestID, candidateAccountID string) (*Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ownerReq, ok := r.requests[requestID]
	if !ok {
		return nil, ErrRequestNotFound
	}
	// Find candidateAccountID's own request that offered ownerReq's account as a candidate.
	for _, req := range r.requests {
		if req.AccountID != candidateAccountID {
			continue
		}
		if c, ok := r.candidates[candidateKey{req.ID, ownerReq.AccountID}]; ok {
			cp := *c
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *MemRepo) AddExclusion(_ context.Context, e Exclusion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := e
	r.exclusions[exclusionKey{e.AccountID, e.ExcludedID}] = &cp
	return nil
}

func (r *MemRepo) IsExcluded(_ context.Context, accountID, otherID string, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	check := func(a, b string) bool {
		e, ok := r.exclusions[exclusionKey{a, b}]
		if !ok {
			return false
		}
		if !e.ExpiresAt.IsZero() && now.After(e.ExpiresAt) {
			return false
		}
		return true
	}
	return check(accountID, otherID) || check(otherID, accountID), nil
}

func (r *MemRepo) SkipCount(_ context.Context, candidateID string, since time.Time) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]bool)
	for k, c := range r.candidates {
		if k.candidateID != candidateID || c.Decision != DecisionSkip || c.OfferedAt.Before(since) {
			continue
		}
		if req, ok := r.requests[k.requestID]; ok {
			seen[req.AccountID] = true
		}
	}
	return len(seen), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
