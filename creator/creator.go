// Package creator implements the creator dashboard domain (doc 03 CRTR_001-3,
// doc 11 §7, roadmap Phase 14): earnings ledger, payout requests gated on
// KYC + hold period + threshold + account standing, and stream/follower
// analytics.
//
// Like match and pv before it, this package never imports ledger, streaming
// or auth directly — the composition root (feed/cmd/api/main.go) adapts
// those services into the closures declared below, so creator stays
// testable without a real ledger or session store.
package creator

import (
	"errors"
	"time"
)

// ─── KYC ────────────────────────────────────────────────────────────────────

type KYCStatus string

const (
	KYCNone     KYCStatus = "none"
	KYCPending  KYCStatus = "pending"
	KYCVerified KYCStatus = "verified"
	KYCRejected KYCStatus = "rejected"
)

type KYCRecord struct {
	AccountID   string     `json:"account_id"`
	Status      KYCStatus  `json:"status"`
	LegalName   string     `json:"legal_name,omitempty"`
	Country     string     `json:"country,omitempty"`
	SubmittedAt time.Time  `json:"submitted_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
}

// ─── Payouts ──────────────────────────────────────────────────────────────

// PayoutStatus mirrors the withdrawal spec's admin-gated state machine:
// a payout sits in PayoutRequested (pending admin review) until a
// finance/superadmin admin explicitly approves or rejects it — it never
// auto-advances. Approved payouts move to PayoutProcessing while the admin
// executes the transfer out-of-band, then to PayoutPaid (with a
// payout_reference) or PayoutFailed (which reverses the reserved balance).
type PayoutStatus string

const (
	PayoutRequested  PayoutStatus = "requested"   // pending admin review
	PayoutApproved   PayoutStatus = "approved"    // admin approved; not yet marked processing
	PayoutRejected   PayoutStatus = "rejected"    // admin rejected; balance reversed to available
	PayoutProcessing PayoutStatus = "processing"  // admin is executing the transfer
	PayoutPaid       PayoutStatus = "paid"        // transfer completed, payout_reference recorded
	PayoutFailed     PayoutStatus = "failed"      // transfer failed; balance reversed to available
	// PayoutCompleted is kept only so any existing stored data/tests using
	// the old terminal-success name still decode; new code should use
	// PayoutPaid instead.
	PayoutCompleted PayoutStatus = "completed"
)

type Payout struct {
	ID             string       `json:"id"`
	AccountID      string       `json:"account_id"`
	AmountDiamonds int64        `json:"amount_diamonds"`
	Status         PayoutStatus `json:"status"`
	FailureReason  string       `json:"failure_reason,omitempty"`
	TransactionID  string       `json:"transaction_id"`
	// ReversalTransactionID is set once a rejected/failed payout's reserved
	// balance has been returned to the creator's available balance via a
	// compensating ledger entry (never a silent balance edit — doc rule 19).
	ReversalTransactionID string     `json:"reversal_transaction_id,omitempty"`
	PayoutReference       string     `json:"payout_reference,omitempty"`
	AdminNote              string     `json:"admin_note,omitempty"`
	ReviewedBy             string     `json:"reviewed_by,omitempty"`
	RequestedAt            time.Time  `json:"requested_at"`
	ReviewedAt             *time.Time `json:"reviewed_at,omitempty"`
	PaidAt                 *time.Time `json:"paid_at,omitempty"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

// ─── Payout profile ─────────────────────────────────────────────────────────

// PayoutMethod is how a creator wants to receive a payout. Kept as a small
// fixed enum rather than a free-text field so the admin console can render
// a consistent payee-detail form per method.
type PayoutMethod string

const (
	PayoutMethodBankTransfer PayoutMethod = "bank_transfer"
	PayoutMethodMobileWallet PayoutMethod = "mobile_wallet" // e.g. JazzCash/Easypaisa in Pakistan
	PayoutMethodPayPal       PayoutMethod = "paypal"
)

// PayoutProfile is where a creator's payee details are recorded so an admin
// knows where to actually send a manually-processed payout. Deliberately
// minimal: no bank login credentials, no card CVV, no payment-provider
// secrets — just enough for a human to execute a transfer out-of-band
// (doc rule 20/21: never store sensitive payment credentials unnecessarily,
// never log them).
type PayoutProfile struct {
	AccountID   string       `json:"account_id"`
	Method      PayoutMethod `json:"method"`
	Country     string       `json:"country"`
	PayeeName   string       `json:"payee_name"`
	// Destination is the single payee identifier for the chosen method:
	// bank account number, mobile wallet number, or PayPal email. Never
	// logged (see handlers — payout profile writes are excluded from
	// request logging) and only ever shown back to its own owner or an
	// admin actually processing a payout.
	Destination string    `json:"destination"`
	UpdatedAt   time.Time `json:"updated_at"`
}

var ErrPayoutProfileRequired = errors.New("creator: a payout profile must be set before requesting a withdrawal")

// ─── Supported payout countries ─────────────────────────────────────────────
//
// The platform launches operated from Pakistan (doc rule 26) but is built
// for worldwide creators (doc rule 0) — rather than hard-coding Pakistan
// throughout the payout path, Pakistan is just one row in this table like
// every other supported country (doc rule 25). A country not listed here,
// or listed with Enabled: false, cannot be set as a payout profile's
// country at all — this is deliberately a stricter gate than just "the
// minimum differs per country": an unsupported country has literally no
// way to receive a payout yet.
type SupportedPayoutCountry struct {
	CountryCode           string         `json:"country_code"`
	Enabled               bool           `json:"enabled"`
	Currency              string         `json:"currency"`
	MinimumWithdrawalDiamonds int64      `json:"minimum_withdrawal_diamonds"`
	AvailableMethods      []PayoutMethod `json:"available_methods"`
	RequiresVerification  bool           `json:"requires_verification"`
}

// PayoutCountries is the fixed dev catalogue (same "fixed Go var, not yet a
// DB-backed admin-editable table" pattern as giftsvc.Catalogue and
// ledger.Products) — a real deployment would move this behind the same
// kind of admin CRUD the roadmap gives gifts and coin packages, but the
// shape here is what that table's rows would look like.
var PayoutCountries = []SupportedPayoutCountry{
	{
		CountryCode: "PK", Enabled: true, Currency: "PKR",
		MinimumWithdrawalDiamonds: PayoutThresholdDiamonds,
		AvailableMethods:          []PayoutMethod{PayoutMethodBankTransfer, PayoutMethodMobileWallet},
		RequiresVerification:      true,
	},
	{
		CountryCode: "US", Enabled: true, Currency: "USD",
		MinimumWithdrawalDiamonds: PayoutThresholdDiamonds,
		AvailableMethods:          []PayoutMethod{PayoutMethodBankTransfer, PayoutMethodPayPal},
		RequiresVerification:      true,
	},
	{
		CountryCode: "GB", Enabled: true, Currency: "GBP",
		MinimumWithdrawalDiamonds: PayoutThresholdDiamonds,
		AvailableMethods:          []PayoutMethod{PayoutMethodBankTransfer, PayoutMethodPayPal},
		RequiresVerification:      true,
	},
	{
		CountryCode: "IN", Enabled: true, Currency: "INR",
		MinimumWithdrawalDiamonds: PayoutThresholdDiamonds,
		AvailableMethods:          []PayoutMethod{PayoutMethodBankTransfer, PayoutMethodMobileWallet},
		RequiresVerification:      true,
	},
	{
		CountryCode: "AE", Enabled: true, Currency: "AED",
		MinimumWithdrawalDiamonds: PayoutThresholdDiamonds,
		AvailableMethods:          []PayoutMethod{PayoutMethodBankTransfer, PayoutMethodPayPal},
		RequiresVerification:      true,
	},
}

func PayoutCountryByCode(code string) (SupportedPayoutCountry, bool) {
	for _, c := range PayoutCountries {
		if c.CountryCode == code {
			return c, true
		}
	}
	return SupportedPayoutCountry{}, false
}

// PayoutMethodAllowed reports whether m is one of c's AvailableMethods.
func PayoutMethodAllowed(c SupportedPayoutCountry, m PayoutMethod) bool {
	for _, allowed := range c.AvailableMethods {
		if allowed == m {
			return true
		}
	}
	return false
}

var (
	// ErrUnsupportedPayoutCountry covers both "we've never heard of this
	// country code" and "we know it but haven't enabled payouts there yet"
	// — deliberately the same message either way (nothing to gain by
	// distinguishing them for the caller).
	ErrUnsupportedPayoutCountry = errors.New("creator: withdrawals are not yet supported for this country")
	ErrUnsupportedPayoutMethod  = errors.New("creator: this payout method is not available for the selected country")
)

// Doc 11 §7's payout gate constants.
const (
	PayoutThresholdDiamonds = 10_000
	PayoutHoldPeriod        = 14 * 24 * time.Hour
)

var (
	ErrKYCIncomplete      = errors.New("creator: KYC not verified")
	ErrAccountRestricted  = errors.New("creator: account restricted")
	ErrBelowThreshold     = errors.New("creator: available balance below payout threshold")
	ErrInvalidAmount      = errors.New("creator: payout amount must be positive and no more than the available (unlocked) balance")
	ErrPayoutNotFound     = errors.New("creator: payout not found")
	ErrKYCAlreadyVerified = errors.New("creator: KYC already verified")
	// ErrInvalidPayoutTransition guards the admin state machine (requested ->
	// approved|rejected -> processing -> paid|failed) so an admin action can
	// never skip a step or act on an already-terminal payout.
	ErrInvalidPayoutTransition = errors.New("creator: payout is not in a state that allows this action")
)

// ─── Earnings ─────────────────────────────────────────────────────────────

// EarningEntry mirrors the subset of a ledger.HistoryItem the creator
// service needs, expressed without importing the ledger package — the
// composition root adapts real ledger.HistoryItem values into this shape.
type EarningEntry struct {
	TransactionID  string
	Kind           string // "gift" | "pv_settle" | "payout" | ...
	AmountDiamonds int64  // signed: negative for payout debits
	GiftID         string // set only when Kind == "gift"
	CreatedAt      time.Time
}

type GiftBreakdownEntry struct {
	GiftID         string `json:"gift_id"`
	Count          int    `json:"count"`
	TotalDiamonds  int64  `json:"total_diamonds"`
}

type EarningsSummary struct {
	TotalEarnedDiamonds   int64                 `json:"total_earned_diamonds"`
	LockedDiamonds        int64                 `json:"locked_diamonds"`
	AvailableDiamonds     int64                 `json:"available_diamonds"`
	PaidOutDiamonds       int64                 `json:"paid_out_diamonds"`
	CurrentBalance        int64                 `json:"current_balance_diamonds"`
	ByGift                []GiftBreakdownEntry  `json:"gift_breakdown"`
	Entries               []EarningEntry        `json:"-"` // never serialized directly; handlers hand-pick fields
}

// ─── Stream analytics ───────────────────────────────────────────────────────

type StreamStat struct {
	SessionID   string
	RoomID      string
	State       string
	StartedAt   *time.Time
	EndedAt     *time.Time
	PeakViewers int
}

type StreamAnalytics struct {
	TotalStreams      int        `json:"total_streams"`
	TotalPeakViewers  int        `json:"total_peak_viewers"`
	AvgPeakViewers    float64    `json:"avg_peak_viewers"`
	LastStreamAt      *time.Time `json:"last_stream_at"`
}

// ─── Follower analytics ─────────────────────────────────────────────────────

type FollowerPoint struct {
	AccountID  string
	FollowedAt time.Time
}

type FollowerAnalytics struct {
	TotalFollowers   int `json:"total_followers"`
	NewLast7Days     int `json:"new_last_7_days"`
	NewLast30Days    int `json:"new_last_30_days"`
}

type Analytics struct {
	Streams   StreamAnalytics   `json:"streams"`
	Followers FollowerAnalytics `json:"followers"`
	Gifts     []GiftBreakdownEntry `json:"gift_breakdown"`
}
