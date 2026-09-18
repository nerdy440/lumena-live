// Package profilesvc is the application service layer for profiles, the social
// graph, and privacy settings. It orchestrates the domain repos and enforces
// cross-cutting rules (e.g., you cannot get a profile for a user who blocked you).
package profilesvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/lumena/profile"
	"github.com/lumena/profile/social"
)

// ProfileView is what a viewer sees when looking at another user's profile.
// Different from profile.Profile: it includes relationship state and
// omits fields the viewer shouldn't see.
type ProfileView struct {
	*profile.Profile
	Counts       *profile.ProfileCounts
	Relationship *social.Relationship
	IsOwner      bool
}

// Service is the profile application service.
type Service struct {
	profileRepo profile.Repo
	privacyRepo profile.PrivacyRepo
	socialRepo  social.Repo
}

func NewService(profileRepo profile.Repo, privacyRepo profile.PrivacyRepo, socialRepo social.Repo) *Service {
	return &Service{
		profileRepo: profileRepo,
		privacyRepo: privacyRepo,
		socialRepo:  socialRepo,
	}
}

// CreateProfile initialises a profile for a new account. repo.Create is
// idempotent (returns the existing profile untouched for an account that
// already has one) — this method deliberately does NOT also write default
// privacy settings, even for a genuinely new profile.
//
// It used to: unconditionally call privacyRepo.Update with
// DefaultPrivacySettings on every call, including from GET /users/{id}'s
// "auto-create a stub if missing" fallback, which runs on every profile
// view. That silently reset a user's who_can_call/who_can_dm/matchable/etc
// back to defaults — not just on repeat calls (repo.Create's own
// idempotency guards against that), but the very first time this ran
// *after* the user had already customized privacy via PATCH /me/privacy
// before their Profile record happened to exist yet (privacy and profile
// are independent records; nothing requires the profile to exist first).
// PrivacyRepo.Get already returns DefaultPrivacySettings transparently
// when no record has been written yet (see MemPrivacyRepo.Get), so this
// write was always redundant, and the only role it ever actually played
// was to clobber legitimately-set custom values written before it ran.
func (s *Service) CreateProfile(ctx context.Context, accountID, displayName, regionCode string) (*profile.Profile, error) {
	p, err := s.profileRepo.Create(ctx, accountID, displayName, regionCode)
	if err != nil {
		return nil, fmt.Errorf("profilesvc: create: %w", err)
	}
	return p, nil
}

// GetProfile returns a profile as seen by viewerID.
// Returns nil, ErrProfileUnavailable if the viewer is blocked.
func (s *Service) GetProfile(ctx context.Context, viewerID, subjectID string) (*ProfileView, error) {
	// Get relationship first — block in either direction returns the same
	// generic "unavailable" response (doc 04 §3: don't reveal block state).
	var rel *social.Relationship
	if viewerID != "" && viewerID != subjectID {
		var err error
		rel, err = s.socialRepo.GetRelationship(ctx, viewerID, subjectID)
		if err != nil {
			return nil, fmt.Errorf("profilesvc: get relationship: %w", err)
		}
		if rel.Blocked || rel.BlockedBy {
			// Identical response whether you blocked them or they blocked you.
			// This prevents using "profile unavailable" as a block-detection oracle.
			return nil, ErrProfileUnavailable
		}
	}

	p, err := s.profileRepo.Get(ctx, subjectID)
	if err != nil {
		if errors.Is(err, profile.ErrNotFound) {
			return nil, ErrProfileUnavailable
		}
		return nil, fmt.Errorf("profilesvc: get profile: %w", err)
	}

	counts, err := s.profileRepo.GetCounts(ctx, subjectID)
	if err != nil {
		counts = &profile.ProfileCounts{AccountID: subjectID}
	}

	return &ProfileView{
		Profile:      p,
		Counts:       counts,
		Relationship: rel,
		IsOwner:      viewerID == subjectID,
	}, nil
}

// UpdateProfile applies a partial update. Only the account owner can do this.
func (s *Service) UpdateProfile(ctx context.Context, accountID string, req profile.UpdateRequest) (*profile.Profile, error) {
	p, err := s.profileRepo.Update(ctx, accountID, req)
	if err != nil {
		return nil, fmt.Errorf("profilesvc: update: %w", err)
	}
	return p, nil
}

// ─── Social graph operations ──────────────────────────────────────────────────

// Follow makes followerID follow followeeID.
// Blocked users cannot follow each other. Block and follow are independent axes.
// Returns the authoritative relationship (BT-03: client renders from this, not from guess).
func (s *Service) Follow(ctx context.Context, followerID, followeeID string) (*social.Relationship, error) {
	if followerID == followeeID {
		return nil, fmt.Errorf("profilesvc: cannot follow yourself")
	}
	rel, err := s.socialRepo.Follow(ctx, followerID, followeeID)
	if err != nil {
		return nil, fmt.Errorf("profilesvc: follow: %w", err)
	}
	return rel, nil
}

// Unfollow removes followerID's follow of followeeID.
// This is a first-class inverse of Follow (BT-02): never requires blocking.
// Returns the authoritative relationship.
func (s *Service) Unfollow(ctx context.Context, followerID, followeeID string) (*social.Relationship, error) {
	if followerID == followeeID {
		return nil, fmt.Errorf("profilesvc: cannot unfollow yourself")
	}
	rel, err := s.socialRepo.Unfollow(ctx, followerID, followeeID)
	if err != nil {
		return nil, fmt.Errorf("profilesvc: unfollow: %w", err)
	}
	return rel, nil
}

// Block blocks blockedID. Independent of follow state.
func (s *Service) Block(ctx context.Context, blockerID, blockedID string) (*social.Relationship, error) {
	rel, err := s.socialRepo.Block(ctx, blockerID, blockedID)
	if err != nil {
		return nil, fmt.Errorf("profilesvc: block: %w", err)
	}
	return rel, nil
}

// Unblock unblocks blockedID.
func (s *Service) Unblock(ctx context.Context, blockerID, blockedID string) (*social.Relationship, error) {
	rel, err := s.socialRepo.Unblock(ctx, blockerID, blockedID)
	if err != nil {
		return nil, fmt.Errorf("profilesvc: unblock: %w", err)
	}
	return rel, nil
}

// GetFollowing returns the accounts that accountID follows.
// When requesterID == accountID (owner view), the read is primary-consistent (BT-03).
func (s *Service) GetFollowing(ctx context.Context, accountID, requesterID, cursor string, limit int) ([]social.FollowEntry, string, error) {
	ownerView := accountID == requesterID
	return s.socialRepo.GetFollowing(ctx, accountID, ownerView, cursor, limit)
}

// GetFollowers returns the accounts that follow accountID.
func (s *Service) GetFollowers(ctx context.Context, accountID, cursor string, limit int) ([]social.FollowEntry, string, error) {
	return s.socialRepo.GetFollowers(ctx, accountID, cursor, limit)
}

// GetRelationship returns the full relationship between two accounts.
func (s *Service) GetRelationship(ctx context.Context, viewerID, subjectID string) (*social.Relationship, error) {
	return s.socialRepo.GetRelationship(ctx, viewerID, subjectID)
}

// ─── Privacy ──────────────────────────────────────────────────────────────────

// GetPrivacySettings returns the privacy settings for an account.
func (s *Service) GetPrivacySettings(ctx context.Context, accountID string) (*profile.PrivacySettings, error) {
	return s.privacyRepo.Get(ctx, accountID)
}

// UpdatePrivacySettings updates privacy settings.
func (s *Service) UpdatePrivacySettings(ctx context.Context, accountID string, settings profile.PrivacySettings) (*profile.PrivacySettings, error) {
	return s.privacyRepo.Update(ctx, accountID, settings)
}

// ─── Errors ───────────────────────────────────────────────────────────────────

// ErrProfileUnavailable is the generic response for: not found, blocked, or suspended.
// It deliberately does not distinguish between these cases (doc 04 §3).
var ErrProfileUnavailable = errors.New("profilesvc: profile is not available")
