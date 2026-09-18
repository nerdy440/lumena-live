package referral

import (
	"context"
	"fmt"
	"sync"
)

type MemRepo struct {
	mu         sync.RWMutex
	claimed    map[string]bool // referredID -> has claimed
	byReferrer map[string][]Referral
	nextID     int
}

func NewMemRepo() *MemRepo {
	return &MemRepo{
		claimed:    make(map[string]bool),
		byReferrer: make(map[string][]Referral),
	}
}

var _ Repo = (*MemRepo)(nil)

func (m *MemRepo) TryClaim(_ context.Context, accountID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimed[accountID] {
		return false, nil
	}
	m.claimed[accountID] = true
	return true, nil
}

// RecordReward assigns r its ID (mutating the caller's struct, not just
// an internal copy) so the response ClaimCode returns to the client
// carries the same ID that ListByReferrer will later show — otherwise
// the immediate claim response would show an empty id.
func (m *MemRepo) RecordReward(_ context.Context, r *Referral) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	r.ID = fmt.Sprintf("ref-%04d", m.nextID)
	// Prepend so ListByReferrer's "most recent first" doesn't need a sort
	// on every read — this repo only ever appends via this one method.
	m.byReferrer[r.ReferrerID] = append([]Referral{*r}, m.byReferrer[r.ReferrerID]...)
	return nil
}

func (m *MemRepo) ListByReferrer(_ context.Context, referrerID string) ([]Referral, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := m.byReferrer[referrerID]
	out := make([]Referral, len(items))
	copy(out, items)
	return out, nil
}
