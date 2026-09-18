// Package fraud implements fraud prevention (doc 02 §J AF-01-06, roadmap
// Phase 18): device/IP fingerprint clustering, gift-loop detection,
// velocity limits, and chargeback tracking — all feeding into the SAME
// risk scoring pipeline moderation's grooming-pattern engine already
// writes to (doc 10 §2d, Phase 15), not a second parallel one.
//
// Like every module since match, this package never imports auth, ledger
// or moderation directly — the composition root adapts those services
// into the closures declared in fraudsvc.
package fraud

import (
	"errors"
	"time"
)

// ─── Device fingerprints (AF-02, doc 06 §12) ────────────────────────────────

type DeviceFingerprint struct {
	AccountID string    `json:"account_id"`
	DeviceHash string   `json:"device_hash"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// DeviceClusterMinAccounts is how many distinct accounts sharing one
// device hash counts as clustering — doc 12 Phase 18's exit gate: "same
// device, multiple accounts clusters correctly."
const DeviceClusterMinAccounts = 2

// ─── Gift loop (AF-03) ──────────────────────────────────────────────────────

// GiftLoopWindow matches doc 12 Phase 18's exit gate exactly: "A→B, B→A
// within 5 minutes elevates risk score."
const GiftLoopWindow = 5 * time.Minute

// ─── Velocity limits (AF-04) ─────────────────────────────────────────────

// Dev-scale limiter constants — doc 02 AF-04 says velocity limits are
// "already deployed" elsewhere (OTP request throttling, match request
// rate limiting, moderation's report rate limit); this is the
// gift-specific one this phase adds, per the roadmap bullet "Signup,
// gift, message, call."
const (
	GiftVelocityWindow = 1 * time.Minute
	GiftVelocityLimit  = 5 // max gifts a single sender can send per window
)

var ErrVelocityExceeded = errors.New("fraud: gift velocity limit exceeded")

// ErrForbidden is returned by the admin-facing device-cluster lookup when
// the caller lacks the fraud.review permission.
var ErrForbidden = errors.New("fraud: fraud.review permission required")

// ─── Chargeback tracking (AF-05) ─────────────────────────────────────────

// ChargebackRiskThreshold is how many disputes from one account elevates
// risk — a single dispute is normal customer behavior, not a signal.
const ChargebackRiskThreshold = 2

// ─── Risk signal weights ─────────────────────────────────────────────────
//
// Same scale as moderation's grooming signals (2-3 points each) — see
// moderation.RiskWatchThreshold / RiskManualReviewThreshold, which these
// signals feed into unchanged.
const (
	SignalDeviceClustering   = 4.0
	SignalGiftLoop           = 4.0
	SignalRepeatChargeback   = 3.0
)
