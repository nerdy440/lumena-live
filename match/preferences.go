package match

import (
	"context"
	"sync"
)

// PreferencesRepo stores each account's default match preferences (doc 07
// §10's GET/PATCH /me/match-preferences) — distinct from the Matchable
// opt-in toggle, which lives in profile.PrivacySettings and is edited via
// the existing PATCH /me/privacy endpoint from Phase 3.
type PreferencesRepo interface {
	Get(ctx context.Context, accountID string) (Preferences, error)
	Set(ctx context.Context, accountID string, prefs Preferences) error
}

type MemPreferencesRepo struct {
	mu   sync.Mutex
	byID map[string]Preferences
}

func NewMemPreferencesRepo() *MemPreferencesRepo {
	return &MemPreferencesRepo{byID: make(map[string]Preferences)}
}

var _ PreferencesRepo = (*MemPreferencesRepo)(nil)

func (r *MemPreferencesRepo) Get(_ context.Context, accountID string) (Preferences, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[accountID], nil
}

func (r *MemPreferencesRepo) Set(_ context.Context, accountID string, prefs Preferences) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[accountID] = prefs
	return nil
}
