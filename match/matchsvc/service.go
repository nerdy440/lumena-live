// Package matchsvc implements the matchmaking business logic (doc 06 §7,
// roadmap Phase 12): server-controlled candidate selection, mutual-connect
// resolution, and the anti-abuse exclusions from MT-04.
//
// Preference filtering is honest about its limits: this dev build stores
// no numeric birthdate (doc 10 §7: DOB is hashed after assurance, never
// kept in queryable form) and no gender field anywhere in the profile
// model, so age/gender preferences are accepted and stored but cannot
// actually filter candidates on real demographic data. The gender-filter
// feature flag mechanism is real and defaults to disabled everywhere
// (doc 10 §7 / roadmap exit gate) — it has nothing to switch on yet, which
// is the correct default, not a workaround.
package matchsvc

import (
	"context"
	"errors"
	"time"

	"github.com/lumena/match"
)

type IsMatchableFunc func(ctx context.Context, accountID string) (bool, error)
type AgeAssuredFunc func(ctx context.Context, accountID string) (bool, error)
type IsBlockedFunc func(ctx context.Context, a, b string) (bool, error)

const (
	rateLimitWindow            = time.Hour
	rateLimitMax               = 10
	skippedRecentTTL           = 24 * time.Hour
	repeatOffenderThreshold    = 5
	repeatOffenderWindow       = 24 * time.Hour
	repeatOffenderExclusionTTL = 24 * time.Hour
)

type Service struct {
	repo        match.Repo
	isMatchable IsMatchableFunc
	ageOK       AgeAssuredFunc
	isBlocked   IsBlockedFunc

	// genderFilterRegions is the feature flag from doc 10 §7: empty means
	// disabled everywhere, matching the required default. A region string
	// present and true is the only way gender preference actually filters
	// candidates — and even then, this build has no gender data to filter
	// on (see package doc), so it remains a no-op until that data exists.
	genderFilterRegions map[string]bool
}

func NewService(repo match.Repo, isMatchable IsMatchableFunc, ageOK AgeAssuredFunc, isBlocked IsBlockedFunc) *Service {
	return &Service{repo: repo, isMatchable: isMatchable, ageOK: ageOK, isBlocked: isBlocked, genderFilterRegions: map[string]bool{}}
}

// CreateRequest starts searching (doc 07 §10's POST /match/requests):
// age-gated, opt-in only. It immediately attempts to pair with a
// compatible request already searching.
func (s *Service) CreateRequest(ctx context.Context, accountID string, prefs match.Preferences) (*match.Request, error) {
	matchable, err := s.isMatchable(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !matchable {
		return nil, match.ErrNotMatchable
	}
	aged, err := s.ageOK(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if !aged {
		return nil, match.ErrNotAgeAssured
	}
	count, err := s.repo.RecentRequestCount(ctx, accountID, time.Now().Add(-rateLimitWindow))
	if err != nil {
		return nil, err
	}
	if count >= rateLimitMax {
		return nil, match.ErrRateLimited
	}

	req, err := s.repo.CreateRequest(ctx, accountID, prefs)
	if err != nil {
		return nil, err
	}
	if err := s.tryMatch(ctx, req); err != nil {
		return nil, err
	}
	return s.repo.GetRequest(ctx, req.ID)
}

// tryMatch scans the searching pool for the first compatible request and,
// if found, offers each side the other as a candidate. Server-controlled:
// the client never selects who it's offered (doc 06 §7 / MT-01).
func (s *Service) tryMatch(ctx context.Context, req *match.Request) error {
	pool, err := s.repo.SearchingRequests(ctx, req.AccountID)
	if err != nil {
		return err
	}
	now := time.Now()
	for i := range pool {
		other := pool[i]

		excluded, err := s.repo.IsExcluded(ctx, req.AccountID, other.AccountID, now)
		if err != nil {
			return err
		}
		if excluded {
			continue
		}
		blocked, err := s.isBlocked(ctx, req.AccountID, other.AccountID)
		if err != nil {
			return err
		}
		if blocked {
			continue
		}
		skipCount, err := s.repo.SkipCount(ctx, other.AccountID, now.Add(-repeatOffenderWindow))
		if err != nil {
			return err
		}
		if skipCount >= repeatOffenderThreshold {
			// Repeat-offender auto-exclusion (MT-04) — record it so future
			// scans short-circuit via IsExcluded instead of recomputing.
			_ = s.repo.AddExclusion(ctx, match.Exclusion{
				AccountID: req.AccountID, ExcludedID: other.AccountID,
				Reason: match.ExclusionSafety, ExpiresAt: now.Add(repeatOffenderExclusionTTL),
			})
			continue
		}
		if !preferencesCompatible(req.Preferences, other.Preferences) {
			continue
		}

		score, reasons := score(req, &other)
		c1 := &match.Candidate{RequestID: req.ID, CandidateID: other.AccountID, Score: score, ScoreReasons: reasons, OfferedAt: now}
		c2 := &match.Candidate{RequestID: other.ID, CandidateID: req.AccountID, Score: score, ScoreReasons: reasons, OfferedAt: now}
		if err := s.repo.AddCandidate(ctx, c1); err != nil {
			return err
		}
		return s.repo.AddCandidate(ctx, c2)
	}
	return nil
}

// preferencesCompatible is intentionally permissive — see package doc.
// Gender preference only ever filters where a region flag is set, which
// nothing in this build sets, so this always returns true today. The
// shape exists so a real deployment with actual profile demographic data
// and configured regions can tighten it without changing callers.
func preferencesCompatible(a, b match.Preferences) bool {
	return true
}

func score(req *match.Request, other *match.Request) (float64, map[string]any) {
	// A real deployment scores on shared interests, activity recency,
	// region proximity, etc. This dev build has none of that signal, so
	// every compatible pair scores equally — reasons are internal-only
	// (match.Candidate.ScoreReasons has no json tag) so this is safe to
	// leave descriptive rather than numeric.
	return 1.0, map[string]any{"basis": "first compatible match in pool (no real scoring signal available)"}
}

// Decide records a connect/skip decision (doc 07 §10's POST
// /match/decisions). A match resolves only once BOTH sides have
// independently decided "connect" on each other — the mutual-connect rule.
func (s *Service) Decide(ctx context.Context, accountID, requestID, candidateID string, decision match.Decision) (*match.Request, error) {
	if decision != match.DecisionConnect && decision != match.DecisionSkip {
		return nil, errors.New("match: decision must be connect or skip")
	}
	req, err := s.repo.GetRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if req.AccountID != accountID {
		return nil, match.ErrNotOwner
	}
	if req.State != match.StateSearching {
		return nil, match.ErrRequestNotSearching
	}
	cand, err := s.repo.GetCandidate(ctx, requestID, candidateID)
	if err != nil {
		return nil, err
	}
	if cand.Decision != "" {
		return nil, match.ErrCandidateNotFound
	}
	if err := s.repo.SetCandidateDecision(ctx, requestID, candidateID, decision); err != nil {
		return nil, err
	}

	if decision == match.DecisionSkip {
		_ = s.repo.AddExclusion(ctx, match.Exclusion{
			AccountID: accountID, ExcludedID: candidateID,
			Reason: match.ExclusionSkippedRecent, ExpiresAt: time.Now().Add(skippedRecentTTL),
		})
		if err := s.tryMatch(ctx, req); err != nil {
			return nil, err
		}
		return s.repo.GetRequest(ctx, requestID)
	}

	// decision == connect: check whether the other side already connected too.
	mirror, err := s.repo.FindMirror(ctx, requestID, candidateID)
	if err != nil {
		return nil, err
	}
	if mirror != nil && mirror.Decision == match.DecisionConnect {
		now := time.Now()
		req.State = match.StateMatched
		req.ResolvedAt = now
		req.MatchedWith = candidateID
		if err := s.repo.UpdateRequest(ctx, req); err != nil {
			return nil, err
		}
		if otherReq, err := s.repo.GetRequest(ctx, mirror.RequestID); err == nil {
			otherReq.State = match.StateMatched
			otherReq.ResolvedAt = now
			otherReq.MatchedWith = accountID
			_ = s.repo.UpdateRequest(ctx, otherReq)
		}
	}
	return s.repo.GetRequest(ctx, requestID)
}

func (s *Service) CancelRequest(ctx context.Context, accountID, requestID string) error {
	req, err := s.repo.GetRequest(ctx, requestID)
	if err != nil {
		return err
	}
	if req.AccountID != accountID {
		return match.ErrNotOwner
	}
	if req.State != match.StateSearching {
		return match.ErrRequestNotSearching
	}
	req.State = match.StateCancelled
	req.ResolvedAt = time.Now()
	return s.repo.UpdateRequest(ctx, req)
}

func (s *Service) GetRequest(ctx context.Context, accountID, requestID string) (*match.Request, error) {
	req, err := s.repo.GetRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if req.AccountID != accountID {
		return nil, match.ErrNotOwner
	}
	return req, nil
}

// GetPendingCandidate returns the candidate currently offered for a
// request, with ScoreReasons already stripped by the struct's json tag —
// callers still must not add ScoreReasons to any hand-built response map.
func (s *Service) GetPendingCandidate(ctx context.Context, accountID, requestID string) (*match.Candidate, error) {
	req, err := s.repo.GetRequest(ctx, requestID)
	if err != nil {
		return nil, err
	}
	if req.AccountID != accountID {
		return nil, match.ErrNotOwner
	}
	return s.repo.GetPendingCandidate(ctx, requestID)
}

func (s *Service) ListHistory(ctx context.Context, accountID string) ([]match.Request, error) {
	return s.repo.ListHistory(ctx, accountID)
}
