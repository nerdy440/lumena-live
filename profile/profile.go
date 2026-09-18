// Package profile defines the user profile domain and its repository interface.
// The interface is the production contract; implementations are:
//   - MemProfileRepo      — for tests and local dev (this package)
//   - pgProfileRepo       — Postgres (postgres/repo.go, built with integration tag)
package profile

import (
	"context"
	"errors"
	"time"
)

// ─── Domain types ─────────────────────────────────────────────────────────────

// AvatarState tracks moderation status of a profile picture.
// A profile picture is a publish action and enters the moderation queue.
type AvatarState string

const (
	AvatarPending  AvatarState = "pending"
	AvatarApproved AvatarState = "approved"
	AvatarRejected AvatarState = "rejected"
)

// Profile is the public-facing user profile.
type Profile struct {
	AccountID   string
	DisplayName string
	Handle      *string // unique, optional username (e.g. @lumena)
	Bio         *string
	AvatarURL   *string
	AvatarState AvatarState
	Languages   []string
	Interests   []string
	RegionCode  string
	Level       int
	IsCreator   bool
	UpdatedAt   time.Time
}

// ProfileCounts holds denormalized social counts.
// Counts may lag; membership (who follows whom) does not.
type ProfileCounts struct {
	AccountID      string `json:"account_id"`
	FollowerCount  int64  `json:"follower_count"`
	FollowingCount int64  `json:"following_count"`
}

// UpdateRequest carries fields that can be changed by the account owner.
type UpdateRequest struct {
	DisplayName *string
	Handle      *string
	Bio         *string
	AvatarURL   *string // when set: avatar enters pending state
	Languages   []string
	Interests   []string
	RegionCode  *string
}

// ─── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrNotFound       = errors.New("profile: not found")
	ErrHandleTaken    = errors.New("profile: handle is already taken")
	ErrInvalidHandle  = errors.New("profile: handle must be 3–30 characters, letters/digits/underscores only")
	ErrDisplayNameLen = errors.New("profile: display name must be 1–50 characters")
)

// ─── Repository interface ─────────────────────────────────────────────────────

// Repo is the data-access contract for profiles.
// All methods must be safe to call concurrently.
type Repo interface {
	// Create initialises a profile row for a newly-registered account.
	// Called immediately after account creation, before onboarding.
	Create(ctx context.Context, accountID, displayName, regionCode string) (*Profile, error)

	// Get returns the profile for the given accountID.
	// Returns ErrNotFound if no profile exists.
	Get(ctx context.Context, accountID string) (*Profile, error)

	// GetByHandle returns the profile for a @handle.
	GetByHandle(ctx context.Context, handle string) (*Profile, error)

	// Update applies a partial update to the profile.
	// Only non-nil fields in the UpdateRequest are changed.
	// Returns ErrHandleTaken if the requested handle is already in use.
	Update(ctx context.Context, accountID string, req UpdateRequest) (*Profile, error)

	// GetCounts returns the follower/following counts for an account.
	GetCounts(ctx context.Context, accountID string) (*ProfileCounts, error)

	// Search returns profiles matching the query string.
	// Matches against display_name and handle, case-insensitive.
	Search(ctx context.Context, query string, limit int, cursor string) ([]Profile, string, error)
}

// PrivacySettings controls who can contact the account owner.
type PrivacySettings struct {
	AccountID    string        `json:"account_id"`
	WhoCanDM     ContactPolicy `json:"who_can_dm"`
	WhoCanCall   ContactPolicy `json:"who_can_call"`
	ShowPresence bool          `json:"show_presence"`
	Discoverable bool          `json:"discoverable"`
	Matchable    bool          `json:"matchable"` // opt-in; default false
}

type ContactPolicy string

const (
	PolicyEveryone ContactPolicy = "everyone"
	PolicyMutuals  ContactPolicy = "mutuals"
	PolicyNobody   ContactPolicy = "nobody"
)

// DefaultPrivacySettings returns safe defaults.
// who_can_call defaults to mutuals — conservative for a product with stranger-video.
func DefaultPrivacySettings(accountID string) PrivacySettings {
	return PrivacySettings{
		AccountID:    accountID,
		WhoCanDM:     PolicyEveryone,
		WhoCanCall:   PolicyMutuals, // safe default; see doc 10 §7
		ShowPresence: true,
		Discoverable: true,
		Matchable:    false, // opt-in
	}
}

// PrivacyRepo is the data-access contract for privacy settings.
type PrivacyRepo interface {
	Get(ctx context.Context, accountID string) (*PrivacySettings, error)
	Update(ctx context.Context, accountID string, settings PrivacySettings) (*PrivacySettings, error)
}
