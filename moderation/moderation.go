// Package moderation implements trust & safety (doc 03 MODR_001-3, doc 10
// §8, roadmap Phase 15): user reports, a moderator triage queue, rule-bound
// enforcements with the "no automated permanent termination" constraint,
// appeals, and grooming-pattern risk scoring.
//
// Like creator and match before it, this package never imports auth,
// streaming or chat directly — the composition root adapts those services
// into the closures declared in modsvc, keeping moderation testable without
// the rest of the stack.
package moderation

import (
	"errors"
	"time"
)

// ─── Reports (MODR_001) ─────────────────────────────────────────────────────

type ReportState string

const (
	ReportOpen      ReportState = "open"
	ReportTriaged   ReportState = "triaged"
	ReportActioned  ReportState = "actioned"
	ReportDismissed ReportState = "dismissed"
)

type Report struct {
	ID          string      `json:"id"`
	ReporterID  string      `json:"reporter_id"`
	SubjectType string      `json:"subject_type"` // user|room|message|pv_session
	SubjectID   string      `json:"subject_id"`
	ReasonCode  string      `json:"reason_code"`
	Detail      string      `json:"detail,omitempty"`
	EvidenceRef string      `json:"evidence_ref,omitempty"`
	State       ReportState `json:"state"`
	CaseID      string      `json:"case_id"`
	CreatedAt   time.Time   `json:"created_at"`
}

// ReportRateLimitWindow bounds mass-report flooding (doc 10 §9's pen-test
// checklist: "rate-limited per reporter per subject per 24h").
const ReportRateLimitWindow = 24 * time.Hour

// ─── Rule catalogue (public, doc 07 §11's GET /policies/rules) ─────────────

type ModerationRule struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Severity    int    `json:"severity"`
	PublicURL   string `json:"public_url"`
}

// ─── Enforcements (doc 06 §11, doc 10 §8 — the whole BT-09 trust story) ────

type EnforcementAction string

const (
	ActionWarn             EnforcementAction = "warn"
	ActionMute             EnforcementAction = "mute"
	ActionRestrictBroadcast EnforcementAction = "restrict_broadcast"
	ActionRestrictPV       EnforcementAction = "restrict_pv"
	ActionSuspend          EnforcementAction = "suspend"
	ActionTerminate        EnforcementAction = "terminate"
)

type Enforcement struct {
	ID            string            `json:"id"`
	AccountID     string            `json:"account_id"`
	RuleID        string            `json:"rule_id"`
	Action        EnforcementAction `json:"action"`
	DurationHours int               `json:"duration_hours,omitempty"`
	EvidenceRef   string            `json:"evidence_ref"`
	DecidedBy     string            `json:"decided_by"` // "auto:<model>" | "human:<mod_id>"
	CaseID        string            `json:"case_id"`
	Appealable    bool              `json:"appealable"`
	CreatedAt     time.Time         `json:"created_at"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
}

var (
	ErrRateLimited                    = errors.New("moderation: you already reported this subject in the last 24 hours")
	ErrRuleNotFound                    = errors.New("moderation: unknown rule id")
	ErrInvalidAction                   = errors.New("moderation: unknown enforcement action")
	ErrAutomatedTerminationForbidden   = errors.New("moderation: permanent termination requires a human decision — schema constraint (doc 06 §11)")
	ErrNotModerator                    = errors.New("moderation: moderator access required")
	ErrReportNotFound                  = errors.New("moderation: report not found")
	ErrEnforcementNotFound             = errors.New("moderation: enforcement not found")
	ErrNotOwner                        = errors.New("moderation: this record does not belong to you")
	ErrNotAppealable                   = errors.New("moderation: this enforcement is not appealable")
	ErrAlreadyAppealed                 = errors.New("moderation: an appeal is already pending for this enforcement")
	ErrAppealNotFound                  = errors.New("moderation: appeal not found")
	// ErrAppealAlreadyDecided guards against a lost-update race: two
	// concurrent DecideAppeal calls on the same appeal (e.g. a double
	// click, or two moderators) must not both succeed — whichever loses
	// the atomic check-and-write below is rejected outright, so an
	// external side effect (reinstating the account) can never fire for a
	// decision that didn't actually stick.
	ErrAppealAlreadyDecided            = errors.New("moderation: this appeal has already been decided")
)

// ─── Appeals (MODR_003) ──────────────────────────────────────────────────────

type AppealState string

const (
	AppealPending    AppealState = "pending"
	AppealUpheld     AppealState = "upheld"
	AppealOverturned AppealState = "overturned"
)

type Appeal struct {
	ID            string      `json:"id"`
	EnforcementID string      `json:"enforcement_id"`
	AccountID     string      `json:"account_id"`
	Statement     string      `json:"statement"`
	State         AppealState `json:"state"`
	ReviewedBy    string      `json:"reviewed_by,omitempty"`
	DecidedAt     *time.Time  `json:"decided_at,omitempty"`
	CaseID        string      `json:"case_id"`
	CreatedAt     time.Time   `json:"created_at"`
}

// ─── Grooming-pattern risk scoring (doc 10 §2d) ─────────────────────────────
//
// "No single signal triggers action; scored together. Output: risk_reasons
// entry, elevated risk_score, optional manual-review flag. Not an automated
// ban." This engine only ever writes a RiskProfile — never an Enforcement.

type ReviewStatus string

const (
	ReviewNone         ReviewStatus = "none"
	ReviewWatch        ReviewStatus = "watch"
	ReviewManualReview ReviewStatus = "manual_review"
)

type RiskProfile struct {
	AccountID    string       `json:"account_id"`
	RiskScore    float64      `json:"risk_score"`
	RiskReasons  []string     `json:"risk_reasons"`
	ReviewStatus ReviewStatus `json:"review_status"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

// Score thresholds gating ReviewStatus — dev-scale constants, not a tuned
// production model (doc 10 §2d explicitly scopes this as signals feeding a
// score, not a real classifier).
const (
	RiskWatchThreshold        = 3.0
	RiskManualReviewThreshold = 6.0
)
