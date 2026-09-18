// Package referral implements a minimal invite/referral reward system:
// every account can share its own account ID as an invite code, and the
// FIRST time a different account redeems that code, the referrer is
// credited a fixed one-time coin bonus via the real ledger (never a
// silent balance edit — same discipline as every other coin-crediting
// path in this codebase). Deliberately simple for an MVP: no tiered
// rewards, no fraud/self-referral-ring detection beyond blocking exact
// self-referral and one-claim-per-account, no expiring codes.
package referral

import (
	"context"
	"errors"
	"time"
)

// RewardCoins is the fixed one-time bonus credited to the referrer when
// their code is redeemed. A flat amount keeps this auditable and simple
// for a local/personal-use MVP — a real deployment might tier this by
// campaign or region.
const RewardCoins int64 = 100

var (
	ErrSelfReferral     = errors.New("referral: cannot redeem your own referral code")
	ErrAlreadyClaimed   = errors.New("referral: this account has already redeemed a referral code")
	ErrCodeNotFound     = errors.New("referral: no account matches this referral code")
)

// Referral records one successful redemption: accountID ReferredID used
// ReferrerID's code and ReferrerID was credited RewardCoins.
type Referral struct {
	ID             string    `json:"id"`
	ReferrerID     string    `json:"referrer_id"`
	ReferredID     string    `json:"referred_id"`
	RewardCoins    int64     `json:"reward_coins"`
	TransactionID  string    `json:"transaction_id"`
	CreatedAt      time.Time `json:"created_at"`
}

// Repo is the referral data-access contract.
//
// TryClaim and RecordReward are deliberately two separate calls, in a
// fixed order (TryClaim, THEN post the ledger reward, THEN
// RecordReward) — see ClaimCode's doc comment on why: it's the only
// ordering that can never double-credit a referrer, even though it can
// rarely "waste" a claim if the ledger post fails after the gate closes.
type Repo interface {
	// TryClaim atomically marks accountID as having redeemed a code, IF
	// it hadn't already — the single race-free gate the whole feature's
	// "exactly once" guarantee rests on. Returns false (no error) if
	// accountID had already claimed.
	TryClaim(ctx context.Context, accountID string) (bool, error)
	// RecordReward appends a successful referral to the referrer's
	// history. Only ever called after TryClaim succeeded AND the reward
	// was actually posted to the ledger — this call itself does no
	// claimed-status re-check, since TryClaim already reserved the slot.
	RecordReward(ctx context.Context, r *Referral) error
	// ListByReferrer returns everyone who redeemed referrerID's code,
	// most recent first.
	ListByReferrer(ctx context.Context, referrerID string) ([]Referral, error)
}
