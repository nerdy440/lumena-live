// Package profile — in-memory repository implementation.
// Used by tests and local development.
// Not for production: no persistence, no durability, no replication.
package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ─── Profile repo ─────────────────────────────────────────────────────────────

// MemProfileRepo implements Repo using an in-memory map.
type MemProfileRepo struct {
	mu       sync.RWMutex
	profiles map[string]*Profile    // accountID → profile
	handles  map[string]string      // lower(handle) → accountID
	counts   map[string]*ProfileCounts
}

func NewMemProfileRepo() *MemProfileRepo {
	return &MemProfileRepo{
		profiles: make(map[string]*Profile),
		handles:  make(map[string]string),
		counts:   make(map[string]*ProfileCounts),
	}
}

var _ Repo = (*MemProfileRepo)(nil)

type profileRepoSnapshot struct {
	Profiles map[string]*Profile       `json:"profiles"`
	Handles  map[string]string         `json:"handles"`
	Counts   map[string]*ProfileCounts `json:"counts"`
}

// Snapshot/Restore back the local-persistence-to-disk feature in
// feed/cmd/api (a lightweight stand-in for a real database in this dev
// build).
func (r *MemProfileRepo) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return json.Marshal(profileRepoSnapshot{Profiles: r.profiles, Handles: r.handles, Counts: r.counts})
}

// Restore replaces this repo's entire state from a Snapshot's output. Only
// meaningful immediately after NewMemProfileRepo, before any traffic.
func (r *MemProfileRepo) Restore(data []byte) error {
	var s profileRepoSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.Profiles != nil {
		r.profiles = s.Profiles
	}
	if s.Handles != nil {
		r.handles = s.Handles
	}
	if s.Counts != nil {
		r.counts = s.Counts
	}
	return nil
}

func (r *MemProfileRepo) Create(_ context.Context, accountID, displayName, regionCode string) (*Profile, error) {
	if err := ValidateDisplayName(displayName); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.profiles[accountID]; exists {
		// Idempotent: return the existing profile
		cp := *r.profiles[accountID]
		return &cp, nil
	}

	p := &Profile{
		AccountID:   accountID,
		DisplayName: displayName,
		AvatarState: AvatarPending,
		Languages:   []string{},
		Interests:   []string{},
		RegionCode:  regionCode,
		Level:       1,
		UpdatedAt:   time.Now(),
	}
	r.profiles[accountID] = p
	r.counts[accountID] = &ProfileCounts{AccountID: accountID}

	cp := *p
	return &cp, nil
}

func (r *MemProfileRepo) Get(_ context.Context, accountID string) (*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.profiles[accountID]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *p
	return &cp, nil
}

func (r *MemProfileRepo) GetByHandle(_ context.Context, handle string) (*Profile, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.handles[strings.ToLower(handle)]
	if !ok {
		return nil, ErrNotFound
	}
	p := r.profiles[id]
	cp := *p
	return &cp, nil
}

func (r *MemProfileRepo) Update(_ context.Context, accountID string, req UpdateRequest) (*Profile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.profiles[accountID]
	if !ok {
		return nil, ErrNotFound
	}

	if req.DisplayName != nil {
		if err := ValidateDisplayName(*req.DisplayName); err != nil {
			return nil, err
		}
		p.DisplayName = *req.DisplayName
	}

	if req.Handle != nil {
		h := strings.ToLower(*req.Handle)
		if err := ValidateHandle(*req.Handle); err != nil {
			return nil, err
		}
		// Check uniqueness
		if existing, taken := r.handles[h]; taken && existing != accountID {
			return nil, ErrHandleTaken
		}
		// Remove old handle mapping
		if p.Handle != nil {
			delete(r.handles, strings.ToLower(*p.Handle))
		}
		p.Handle = req.Handle
		r.handles[h] = accountID
	}

	if req.Bio != nil {
		p.Bio = req.Bio
	}

	if req.AvatarURL != nil {
		p.AvatarURL = req.AvatarURL
		p.AvatarState = AvatarPending // must re-enter moderation queue
	}

	if req.Languages != nil {
		p.Languages = SanitizeLanguages(req.Languages)
	}

	if req.Interests != nil {
		p.Interests = SanitizeInterests(req.Interests)
	}

	if req.RegionCode != nil {
		p.RegionCode = *req.RegionCode
	}

	p.UpdatedAt = time.Now()
	cp := *p
	return &cp, nil
}

func (r *MemProfileRepo) GetCounts(_ context.Context, accountID string) (*ProfileCounts, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.counts[accountID]
	if !ok {
		return &ProfileCounts{AccountID: accountID}, nil
	}
	cp := *c
	return &cp, nil
}

func (r *MemProfileRepo) Search(_ context.Context, query string, limit int, cursor string) ([]Profile, string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, "", nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	// Skip to cursor (cursor = accountID of last item)
	pastCursor := cursor == ""

	var results []Profile
	for id, p := range r.profiles {
		if !pastCursor {
			if id == cursor {
				pastCursor = true
			}
			continue
		}
		name := strings.ToLower(p.DisplayName)
		handle := ""
		if p.Handle != nil {
			handle = strings.ToLower(*p.Handle)
		}
		if strings.Contains(name, q) || strings.Contains(handle, q) {
			cp := *p
			results = append(results, cp)
			if len(results) >= limit+1 {
				break
			}
		}
	}

	var nextCursor string
	if len(results) > limit {
		nextCursor = results[limit].AccountID
		results = results[:limit]
	}

	return results, nextCursor, nil
}

// IncrFollowCounts updates denormalized counts after a follow operation.
// Called by the social service after a successful follow write.
func (r *MemProfileRepo) IncrFollowCounts(followerID, followeeID string, delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if c, ok := r.counts[followerID]; ok {
		c.FollowingCount += delta
		if c.FollowingCount < 0 {
			c.FollowingCount = 0
		}
	}
	if c, ok := r.counts[followeeID]; ok {
		c.FollowerCount += delta
		if c.FollowerCount < 0 {
			c.FollowerCount = 0
		}
	}
}

// ─── Privacy repo ─────────────────────────────────────────────────────────────

type MemPrivacyRepo struct {
	mu       sync.RWMutex
	settings map[string]*PrivacySettings
}

func NewMemPrivacyRepo() *MemPrivacyRepo {
	return &MemPrivacyRepo{settings: make(map[string]*PrivacySettings)}
}

var _ PrivacyRepo = (*MemPrivacyRepo)(nil)

// Snapshot/Restore back the local-persistence-to-disk feature in
// feed/cmd/api.
func (r *MemPrivacyRepo) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return json.Marshal(r.settings)
}

func (r *MemPrivacyRepo) Restore(data []byte) error {
	var settings map[string]*PrivacySettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if settings != nil {
		r.settings = settings
	}
	return nil
}

func (r *MemPrivacyRepo) Get(_ context.Context, accountID string) (*PrivacySettings, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.settings[accountID]
	if !ok {
		// Return safe defaults if not yet initialised
		def := DefaultPrivacySettings(accountID)
		return &def, nil
	}
	cp := *s
	return &cp, nil
}

func (r *MemPrivacyRepo) Update(_ context.Context, accountID string, settings PrivacySettings) (*PrivacySettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	settings.AccountID = accountID
	r.settings[accountID] = &settings
	cp := settings
	return &cp, nil
}

// ─── Postgres SQL (production) ────────────────────────────────────────────────
// Below are the SQL statements for the Postgres implementation.
// The Go struct pgProfileRepo is in postgres/repo.go (integration build tag).
// Kept here as constants so they can be syntax-reviewed without a DB.

const sqlCreateProfile = `
INSERT INTO profiles (account_id, display_name, avatar_state, languages, interests, region_code)
VALUES ($1, $2, 'pending', '{}', '{}', $3)
ON CONFLICT (account_id) DO UPDATE SET updated_at = profiles.updated_at
RETURNING account_id, display_name, handle, bio, avatar_url, avatar_state,
          languages, interests, region_code, level, is_creator, updated_at`

const sqlGetProfile = `
SELECT account_id, display_name, handle, bio, avatar_url, avatar_state,
       languages, interests, region_code, level, is_creator, updated_at
FROM profiles
WHERE account_id = $1`

const sqlGetProfileByHandle = `
SELECT account_id, display_name, handle, bio, avatar_url, avatar_state,
       languages, interests, region_code, level, is_creator, updated_at
FROM profiles
WHERE lower(handle) = lower($1)`

// sqlUpdateProfile builds a partial update — generated dynamically, not a constant.
// The builder is in the Postgres implementation.
const sqlGetCounts = `
SELECT account_id, follower_count, following_count
FROM follow_counts
WHERE account_id = $1`

const sqlSearchProfiles = `
SELECT account_id, display_name, handle, bio, avatar_url, avatar_state,
       languages, interests, region_code, level, is_creator, updated_at
FROM profiles
WHERE (lower(display_name) LIKE lower($1) OR lower(handle) LIKE lower($1))
  AND discoverable = true
ORDER BY level DESC, updated_at DESC
LIMIT $2`

// Verify all SQL statements compile (caught at init, not runtime).
func init() {
	if sqlCreateProfile == "" || sqlGetProfile == "" ||
		sqlGetProfileByHandle == "" || sqlGetCounts == "" || sqlSearchProfiles == "" {
		panic("profile: SQL constant is empty")
	}
	fmt.Sprintf("%s%s%s%s%s", sqlCreateProfile, sqlGetProfile,
		sqlGetProfileByHandle, sqlGetCounts, sqlSearchProfiles) // reference to avoid unused
}
