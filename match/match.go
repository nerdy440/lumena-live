// Package match implements the matchmaking domain (doc 06 §7, doc 07 §10,
// roadmap Phase 12, feature MT-01 through MT-04): a request enters the
// searching pool, the server (never the client) offers a candidate, and
// both sides must independently decide "connect" before a match resolves.
package match

import (
	"errors"
	"time"
)

type RequestState string

const (
	StateSearching RequestState = "searching"
	StateMatched   RequestState = "matched"
	StateCancelled RequestState = "cancelled"
	StateTimeout   RequestState = "timeout"
)

type Decision string

const (
	DecisionConnect Decision = "connect"
	DecisionSkip    Decision = "skip"
	DecisionExpired Decision = "expired"
)

type ExclusionReason string

const (
	ExclusionBlocked       ExclusionReason = "blocked"
	ExclusionReported      ExclusionReason = "reported"
	ExclusionSkippedRecent ExclusionReason = "skipped_recent"
	ExclusionSafety        ExclusionReason = "safety" // repeat-offender auto-exclusion, MT-04
)

// Preferences are client-supplied filters. GenderPreference is honored only
// where a region feature flag enables it (doc 10 §7) — everywhere else it
// is accepted but ignored, never an error, so the client doesn't need to
// know the region's legal status.
type Preferences struct {
	GenderPreference string `json:"gender_preference,omitempty"`
	MinAge           int    `json:"min_age,omitempty"`
	MaxAge           int    `json:"max_age,omitempty"`
}

type Request struct {
	ID          string       `json:"id"`
	AccountID   string       `json:"account_id"`
	Preferences Preferences  `json:"preferences"`
	State       RequestState `json:"state"`
	CreatedAt   time.Time    `json:"created_at"`
	ResolvedAt  time.Time    `json:"resolved_at,omitempty"`
	MatchedWith string       `json:"matched_with,omitempty"` // set once State == matched
}

// Candidate is one offer within a request — doc 06's match_candidates.
// ScoreReasons is deliberately unexported-shaped for the API: the JSON tag
// is absent so encoding/json can never serialize it regardless of which
// handler touches this struct (doc 06 §7: "the API serializer... has an
// explicit deny-list including this column, and there is a test asserting
// it" — here the deny-list is structural, not a maintained list).
type Candidate struct {
	RequestID    string
	CandidateID  string
	Score        float64
	ScoreReasons map[string]any `json:"-"`
	OfferedAt    time.Time
	Decision     Decision
}

type Exclusion struct {
	AccountID  string
	ExcludedID string
	Reason     ExclusionReason
	ExpiresAt  time.Time // zero = never expires
}

var (
	ErrNotMatchable       = errors.New("match: account has not opted in to matching")
	ErrNotAgeAssured      = errors.New("match: age assurance required for matching")
	ErrRateLimited        = errors.New("match: too many match requests — try again later")
	ErrRequestNotFound    = errors.New("match: request not found")
	ErrNotOwner           = errors.New("match: request does not belong to this account")
	ErrRequestNotSearching = errors.New("match: request is not currently searching")
	ErrCandidateNotFound  = errors.New("match: candidate not found or not currently offered")
)
