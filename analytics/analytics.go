// Package analytics implements the analytics pipeline and standard
// dashboards (doc 02 PL-04, roadmap Phase 17): DAU/MAU, retention, funnel
// analysis, economy summary, and creator trends.
//
// Doc 05's architecture routes this through Kafka into a warehouse; this
// dev build has neither, so EventRepo is the local stand-in — the same
// category of infrastructure gap as every other DevXxx in this codebase
// (dev-topup, DevIngestService, etc.), just expressed as an in-process
// event log instead of a single escalation call. Real events (gifts,
// broadcasts) are read live from the ledger/streaming repos rather than
// re-logged here, so the roadmap's exit gate — "dashboard data matches
// production counts within 1%" — holds exactly (0% drift) by construction:
// there is no separate copy of that data to drift from the source of truth.
package analytics

import (
	"errors"
	"time"
)

// ─── Event pipeline ─────────────────────────────────────────────────────────

type EventType string

const (
	EventAccountCreated EventType = "account_created"
	EventLogin           EventType = "login"
)

// Event is one entry in the account-activity log — fed only by authsvc's
// AuthEventHook (registration and login). Gift/broadcast activity is read
// directly from ledger/streaming instead of duplicated here — see the
// package doc comment.
type Event struct {
	ID        string    `json:"id"`
	Type      EventType `json:"type"`
	AccountID string    `json:"account_id"`
	CreatedAt time.Time `json:"created_at"`
}

// ─── Dashboards ─────────────────────────────────────────────────────────────

type DAUMAU struct {
	DAU  int       `json:"dau"`
	MAU  int       `json:"mau"`
	AsOf time.Time `json:"as_of"`
}

// RetentionPoint is one day-offset of a cohort retention curve — "of the
// accounts that registered on CohortDate, what fraction were active again
// exactly DaysAfter days later."
type RetentionPoint struct {
	DaysAfter     int     `json:"days_after"`
	CohortSize    int     `json:"cohort_size"`
	RetainedCount int     `json:"retained_count"`
	RetainedPct   float64 `json:"retained_pct"`
}

// FunnelStep is one stage of the onboarding funnel (doc 12 Phase 17:
// "onboarding, first gift, first broadcast"), each a running count of
// distinct accounts that have ever reached that stage.
type FunnelStep struct {
	Name       string  `json:"name"`
	Count      int     `json:"count"`
	PctOfFirst float64 `json:"pct_of_first"`
}

type EconomySummary struct {
	RangeDays          int   `json:"range_days"`
	TotalGiftsSent     int   `json:"total_gifts_sent"`
	TotalCoinsSpent    int64 `json:"total_coins_spent"`
	TotalDiamondsIssued int64 `json:"total_diamonds_issued"`
	TotalPurchases     int   `json:"total_purchases"`
	TotalCoinsPurchased int64 `json:"total_coins_purchased"`
}

// CreatorTrendPoint is one day of a creator's gift/broadcast trend line
// (doc 07 §12 GET /creator/analytics?range=).
type CreatorTrendPoint struct {
	Date            string `json:"date"` // YYYY-MM-DD
	GiftsReceived   int    `json:"gifts_received"`
	DiamondsEarned  int64  `json:"diamonds_earned"`
	BroadcastCount  int    `json:"broadcast_count"`
}

var ErrForbidden = errors.New("analytics: this role does not have permission to view analytics")
