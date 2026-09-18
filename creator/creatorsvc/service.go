// Package creatorsvc implements the creator dashboard service (roadmap
// Phase 14): earnings summary, payout requests, and analytics.
package creatorsvc

import (
	"context"
	"time"

	"github.com/lumena/creator"
)

// ─── Injected closures (composition root adapts real services into these) ──

// EarningsHistoryFunc returns every ledger entry ever posted to accountID's
// diamonds account, in any order — the composition root adapts
// ledger.Repo.History(ctx, ledger.UserDiamondsAccount(accountID), ...).
type EarningsHistoryFunc func(ctx context.Context, accountID string) ([]creator.EarningEntry, error)

// DiamondBalanceFunc returns accountID's current diamonds balance —
// adapts ledger.Repo.Balance.
type DiamondBalanceFunc func(ctx context.Context, accountID string) (int64, error)

// AccountRestrictedFunc reports whether accountID currently has an active
// restriction/suspension — adapts authsvc.Service.GetAccountStatus.
type AccountRestrictedFunc func(ctx context.Context, accountID string) (bool, error)

// PostPayoutFunc posts the actual ledger transaction moving amount diamonds
// out of accountID's diamonds account into the platform payout queue —
// adapts ledger.Repo.PostTransaction. Idempotent on idempotencyKey exactly
// like every other money-moving call in this codebase.
type PostPayoutFunc func(ctx context.Context, accountID string, amount int64, idempotencyKey string) (transactionID string, err error)

// ReversePayoutFunc posts the compensating ledger transaction returning a
// rejected/failed payout's held amount to accountID's available diamonds
// balance — the mirror image of PostPayoutFunc. Never a silent balance
// edit: this always goes through the ledger as its own transaction, so the
// reversal is as auditable as the original debit (doc rules 15/19).
type ReversePayoutFunc func(ctx context.Context, accountID string, amount int64, idempotencyKey string) (transactionID string, err error)

// ListSessionsFunc returns every stream session a host has broadcast —
// adapts streaming.MemSessionRepo.ListByHost.
type ListSessionsFunc func(ctx context.Context, hostID string) ([]creator.StreamStat, error)

// FollowerHistoryFunc returns every follow relationship pointed at
// accountID with its timestamp — adapts profile/social.Repo.GetFollowers.
type FollowerHistoryFunc func(ctx context.Context, accountID string) ([]creator.FollowerPoint, error)

// ─── Repos ──────────────────────────────────────────────────────────────────

type KYCRepo interface {
	Get(ctx context.Context, accountID string) (*creator.KYCRecord, error)
	Submit(ctx context.Context, accountID, legalName, country string) (*creator.KYCRecord, error)
	Decide(ctx context.Context, accountID string, status creator.KYCStatus) (*creator.KYCRecord, error)
}

type PayoutRepo interface {
	Create(ctx context.Context, p creator.Payout) (*creator.Payout, error)
	UpdateStatus(ctx context.Context, id string, status creator.PayoutStatus, failureReason string) error
	// Update applies mutate to the stored payout atomically — used by every
	// admin state transition so reads-then-writes of the same record never
	// race (doc rule 13).
	Update(ctx context.Context, id string, mutate func(*creator.Payout) error) error
	Get(ctx context.Context, id string) (*creator.Payout, error)
	ListByAccount(ctx context.Context, accountID string) ([]creator.Payout, error)
	// ListAll is the admin withdrawal queue: every payout across every
	// account, optionally filtered to one status ("" = all).
	ListAll(ctx context.Context, statusFilter creator.PayoutStatus) ([]creator.Payout, error)
	PendingOrProcessingTotal(ctx context.Context, accountID string) (int64, error)
}

// PayoutProfileRepo stores where a creator wants withdrawals sent.
type PayoutProfileRepo interface {
	Get(ctx context.Context, accountID string) (*creator.PayoutProfile, error)
	Set(ctx context.Context, p creator.PayoutProfile) (*creator.PayoutProfile, error)
}

// ─── Service ────────────────────────────────────────────────────────────────

type Service struct {
	kyc            KYCRepo
	payouts        PayoutRepo
	payoutProfiles PayoutProfileRepo

	earningsHistory EarningsHistoryFunc
	diamondBalance  DiamondBalanceFunc
	accountRestricted AccountRestrictedFunc
	postPayout      PostPayoutFunc
	reversePayout   ReversePayoutFunc
	listSessions    ListSessionsFunc
	followerHistory FollowerHistoryFunc

	now func() time.Time
}

func New(
	kyc KYCRepo,
	payouts PayoutRepo,
	payoutProfiles PayoutProfileRepo,
	earningsHistory EarningsHistoryFunc,
	diamondBalance DiamondBalanceFunc,
	accountRestricted AccountRestrictedFunc,
	postPayout PostPayoutFunc,
	reversePayout ReversePayoutFunc,
	listSessions ListSessionsFunc,
	followerHistory FollowerHistoryFunc,
) *Service {
	return &Service{
		kyc:               kyc,
		payouts:           payouts,
		payoutProfiles:    payoutProfiles,
		earningsHistory:   earningsHistory,
		diamondBalance:    diamondBalance,
		accountRestricted: accountRestricted,
		postPayout:        postPayout,
		reversePayout:     reversePayout,
		listSessions:      listSessions,
		followerHistory:   followerHistory,
		now:               time.Now,
	}
}

// ─── KYC ────────────────────────────────────────────────────────────────────

func (s *Service) GetKYCStatus(ctx context.Context, accountID string) (*creator.KYCRecord, error) {
	return s.kyc.Get(ctx, accountID)
}

func (s *Service) SubmitKYC(ctx context.Context, accountID, legalName, country string) (*creator.KYCRecord, error) {
	existing, err := s.kyc.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if existing.Status == creator.KYCVerified {
		return nil, creator.ErrKYCAlreadyVerified
	}
	return s.kyc.Submit(ctx, accountID, legalName, country)
}

// DevApproveKYC force-verifies a pending KYC submission, standing in for
// the real identity/sanctions-screening vendor (doc 02 ID-09) that this
// local dev build doesn't have — same dev-escalation pattern as
// authsvc.Service.DevAssureAge. NEVER ships to production.
func (s *Service) DevApproveKYC(ctx context.Context, accountID string) (*creator.KYCRecord, error) {
	return s.kyc.Decide(ctx, accountID, creator.KYCVerified)
}

// ─── Earnings ─────────────────────────────────────────────────────────────

func (s *Service) GetEarningsSummary(ctx context.Context, accountID string) (*creator.EarningsSummary, error) {
	entries, err := s.earningsHistory(ctx, accountID)
	if err != nil {
		return nil, err
	}
	balance, err := s.diamondBalance(ctx, accountID)
	if err != nil {
		return nil, err
	}

	cutoff := s.now().Add(-creator.PayoutHoldPeriod)
	summary := &creator.EarningsSummary{CurrentBalance: balance, Entries: entries}
	giftTotals := map[string]*creator.GiftBreakdownEntry{}

	for _, e := range entries {
		switch {
		case e.AmountDiamonds > 0:
			summary.TotalEarnedDiamonds += e.AmountDiamonds
			if e.CreatedAt.Before(cutoff) {
				summary.AvailableDiamonds += e.AmountDiamonds
			} else {
				summary.LockedDiamonds += e.AmountDiamonds
			}
			if e.Kind == "gift" && e.GiftID != "" {
				g, ok := giftTotals[e.GiftID]
				if !ok {
					g = &creator.GiftBreakdownEntry{GiftID: e.GiftID}
					giftTotals[e.GiftID] = g
				}
				g.Count++
				g.TotalDiamonds += e.AmountDiamonds
			}
		case e.AmountDiamonds < 0:
			summary.PaidOutDiamonds += -e.AmountDiamonds
		}
	}

	// Available can never exceed the actual current balance — a payout
	// already in flight (requested/processing) has debited the ledger but
	// its diamonds are still counted as "unlocked" by age, so cap at what's
	// really still sitting in the account.
	if summary.AvailableDiamonds > balance {
		summary.AvailableDiamonds = balance
	}
	if summary.AvailableDiamonds < 0 {
		summary.AvailableDiamonds = 0
	}

	for _, g := range giftTotals {
		summary.ByGift = append(summary.ByGift, *g)
	}
	return summary, nil
}

// ─── Payout profile ─────────────────────────────────────────────────────────

func (s *Service) GetPayoutProfile(ctx context.Context, accountID string) (*creator.PayoutProfile, error) {
	return s.payoutProfiles.Get(ctx, accountID)
}

// SetPayoutProfile validates country/method against creator.PayoutCountries
// before storing anything — a creator can only set a profile for a country
// the platform actually supports payouts to, and a method that country
// actually offers (doc rule 25).
func (s *Service) SetPayoutProfile(ctx context.Context, accountID string, method creator.PayoutMethod, country, payeeName, destination string) (*creator.PayoutProfile, error) {
	c, ok := creator.PayoutCountryByCode(country)
	if !ok || !c.Enabled {
		return nil, creator.ErrUnsupportedPayoutCountry
	}
	if !creator.PayoutMethodAllowed(c, method) {
		return nil, creator.ErrUnsupportedPayoutMethod
	}
	return s.payoutProfiles.Set(ctx, creator.PayoutProfile{
		AccountID: accountID, Method: method, Country: country,
		PayeeName: payeeName, Destination: destination,
	})
}

// ─── Payouts ──────────────────────────────────────────────────────────────

// RequestPayout enforces doc 11 §7's payout gates in order — KYC, payout
// profile, threshold, hold period (folded into "available" by
// GetEarningsSummary), and account standing — then posts the ledger
// transaction and returns the created Payout. amount must be positive and
// no more than the available balance net of any payout already in flight.
//
// The created payout starts in PayoutRequested and stays there until a
// finance/superadmin admin explicitly approves or rejects it (see the
// Admin* methods below) — there is no automatic advancement. A real
// deployment's payment rail runs behind the admin action, not on a timer.
func (s *Service) RequestPayout(ctx context.Context, accountID string, amount int64, idempotencyKey string) (*creator.Payout, error) {
	kyc, err := s.kyc.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if kyc.Status != creator.KYCVerified {
		return nil, creator.ErrKYCIncomplete
	}

	profile, err := s.payoutProfiles.Get(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if profile.Method == "" {
		return nil, creator.ErrPayoutProfileRequired
	}

	restricted, err := s.accountRestricted(ctx, accountID)
	if err != nil {
		return nil, err
	}
	if restricted {
		return nil, creator.ErrAccountRestricted
	}

	summary, err := s.GetEarningsSummary(ctx, accountID)
	if err != nil {
		return nil, err
	}
	inFlight, err := s.payouts.PendingOrProcessingTotal(ctx, accountID)
	if err != nil {
		return nil, err
	}
	available := summary.AvailableDiamonds - inFlight
	if available < 0 {
		available = 0
	}

	// The minimum is per-country (doc rule 25) — fall back to the global
	// default if the profile's country was since disabled/removed from the
	// catalogue, rather than letting a stale profile bypass the gate.
	minimum := int64(creator.PayoutThresholdDiamonds)
	if c, ok := creator.PayoutCountryByCode(profile.Country); ok && c.MinimumWithdrawalDiamonds > 0 {
		minimum = c.MinimumWithdrawalDiamonds
	}
	if available < minimum {
		return nil, creator.ErrBelowThreshold
	}
	if amount <= 0 || amount > available {
		return nil, creator.ErrInvalidAmount
	}

	txID, err := s.postPayout(ctx, accountID, amount, idempotencyKey)
	if err != nil {
		return nil, err
	}

	p, err := s.payouts.Create(ctx, creator.Payout{
		AccountID:      accountID,
		AmountDiamonds: amount,
		Status:         creator.PayoutRequested,
		TransactionID:  txID,
		RequestedAt:    s.now(),
		UpdatedAt:      s.now(),
	})
	if err != nil {
		return nil, err
	}

	// Requested is the terminal state until an admin acts (see Admin*
	// methods below) — this build has no real payment rail to hand the
	// request off to, so it deliberately does not auto-advance.
	return p, nil
}

func (s *Service) ListPayouts(ctx context.Context, accountID string) ([]creator.Payout, error) {
	return s.payouts.ListByAccount(ctx, accountID)
}

func (s *Service) GetPayout(ctx context.Context, accountID, payoutID string) (*creator.Payout, error) {
	p, err := s.payouts.Get(ctx, payoutID)
	if err != nil {
		return nil, err
	}
	if p.AccountID != accountID {
		return nil, creator.ErrPayoutNotFound
	}
	return p, nil
}

// ─── Admin withdrawal management ───────────────────────────────────────────
//
// These methods trust that the caller (adminsvc) has already checked the
// admin.PermPayoutManage permission — same convention as every other
// cross-module admin closure in this codebase (e.g. authsvc.SetAccountStatus
// trusts adminsvc.RestrictAccount already gated the call). No permission
// check happens here, and adminID is recorded on the payout purely for the
// audit trail, not as an authorization decision.

// AdminListPayouts is the withdrawal-management queue: every payout across
// every account, optionally filtered to one status.
func (s *Service) AdminListPayouts(ctx context.Context, statusFilter creator.PayoutStatus) ([]creator.Payout, error) {
	return s.payouts.ListAll(ctx, statusFilter)
}

// AdminApprovePayout moves a pending-review payout to Approved — the first
// half of the manual-payout MVP (doc rule 18): approving does not itself
// move money, it just signals the admin has reviewed and intends to pay it.
func (s *Service) AdminApprovePayout(ctx context.Context, adminID, payoutID, note string) (*creator.Payout, error) {
	return s.transitionPayout(ctx, payoutID, []creator.PayoutStatus{creator.PayoutRequested}, func(p *creator.Payout) error {
		p.Status = creator.PayoutApproved
		p.ReviewedBy = adminID
		p.AdminNote = note
		return nil
	})
}

// AdminRejectPayout rejects a payout that hasn't been paid yet and reverses
// its reserved amount back to the creator's available balance via a
// compensating ledger transaction — never a silent balance edit.
func (s *Service) AdminRejectPayout(ctx context.Context, adminID, payoutID, reason string) (*creator.Payout, error) {
	return s.rejectOrFail(ctx, adminID, payoutID, reason, creator.PayoutRejected,
		[]creator.PayoutStatus{creator.PayoutRequested, creator.PayoutApproved})
}

// AdminMarkProcessing records that the admin is now executing the transfer
// out-of-band (doc rule 18).
func (s *Service) AdminMarkProcessing(ctx context.Context, adminID, payoutID string) (*creator.Payout, error) {
	return s.transitionPayout(ctx, payoutID, []creator.PayoutStatus{creator.PayoutApproved}, func(p *creator.Payout) error {
		p.Status = creator.PayoutProcessing
		p.ReviewedBy = adminID
		return nil
	})
}

// AdminMarkPaid closes out a processing payout once the admin has actually
// sent the money, recording the payout_reference for the audit trail (doc
// rule 18 — "who marked it paid, when paid, payout reference").
func (s *Service) AdminMarkPaid(ctx context.Context, adminID, payoutID, payoutReference string) (*creator.Payout, error) {
	if payoutReference == "" {
		return nil, creator.ErrInvalidPayoutTransition
	}
	now := s.now()
	return s.transitionPayout(ctx, payoutID, []creator.PayoutStatus{creator.PayoutProcessing}, func(p *creator.Payout) error {
		p.Status = creator.PayoutPaid
		p.ReviewedBy = adminID
		p.PayoutReference = payoutReference
		p.PaidAt = &now
		return nil
	})
}

// AdminMarkFailed fails a payout that was being processed (e.g. the
// transfer bounced) and reverses its reserved amount back to the creator's
// available balance (doc rule 19).
func (s *Service) AdminMarkFailed(ctx context.Context, adminID, payoutID, reason string) (*creator.Payout, error) {
	return s.rejectOrFail(ctx, adminID, payoutID, reason, creator.PayoutFailed,
		[]creator.PayoutStatus{creator.PayoutProcessing})
}

// rejectOrFail is the shared reject/fail path: both transitions reverse the
// held amount back to available via the ledger, never by editing the
// payout's amount or the creator's balance directly.
//
// The status re-check and the ledger reversal call both happen inside the
// SAME Update critical section — not check-then-reverse-then-recheck —
// specifically so a concurrent admin action on the same payout (e.g. two
// reject clicks, or a race with MarkProcessing) can never post a real
// ledger reversal and then have the status transition itself rejected,
// which would leave the ledger showing money returned while the payout
// record still claims to be in flight (doc rule 13: balance-changing
// operations must be atomic).
func (s *Service) rejectOrFail(ctx context.Context, adminID, payoutID, reason string, terminal creator.PayoutStatus, from []creator.PayoutStatus) (*creator.Payout, error) {
	now := s.now()
	err := s.payouts.Update(ctx, payoutID, func(p *creator.Payout) error {
		if !statusIn(p.Status, from) {
			return creator.ErrInvalidPayoutTransition
		}
		reversalTxID, err := s.reversePayout(ctx, p.AccountID, p.AmountDiamonds, "payout-reversal:"+payoutID)
		if err != nil {
			return err
		}
		p.Status = terminal
		p.ReviewedBy = adminID
		p.AdminNote = reason
		p.FailureReason = reason
		p.ReversalTransactionID = reversalTxID
		p.ReviewedAt = &now
		p.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.payouts.Get(ctx, payoutID)
}

// transitionPayout applies a status-guarded mutation: it verifies the
// payout is currently in one of the allowed `from` states before letting
// mutate run, so a stale/duplicate admin action can never skip a step in
// the approve -> processing -> paid state machine.
func (s *Service) transitionPayout(ctx context.Context, payoutID string, from []creator.PayoutStatus, mutate func(*creator.Payout) error) (*creator.Payout, error) {
	p, err := s.payouts.Get(ctx, payoutID)
	if err != nil {
		return nil, err
	}
	if !statusIn(p.Status, from) {
		return nil, creator.ErrInvalidPayoutTransition
	}
	now := s.now()
	if err := s.payouts.Update(ctx, payoutID, func(p *creator.Payout) error {
		if !statusIn(p.Status, from) {
			return creator.ErrInvalidPayoutTransition
		}
		if err := mutate(p); err != nil {
			return err
		}
		if p.ReviewedAt == nil {
			p.ReviewedAt = &now
		}
		p.UpdatedAt = now
		return nil
	}); err != nil {
		return nil, err
	}
	return s.payouts.Get(ctx, payoutID)
}

func statusIn(s creator.PayoutStatus, set []creator.PayoutStatus) bool {
	for _, x := range set {
		if s == x {
			return true
		}
	}
	return false
}

// ─── Analytics ────────────────────────────────────────────────────────────

func (s *Service) GetAnalytics(ctx context.Context, accountID string) (*creator.Analytics, error) {
	sessions, err := s.listSessions(ctx, accountID)
	if err != nil {
		return nil, err
	}
	followers, err := s.followerHistory(ctx, accountID)
	if err != nil {
		return nil, err
	}
	summary, err := s.GetEarningsSummary(ctx, accountID)
	if err != nil {
		return nil, err
	}

	a := &creator.Analytics{Gifts: summary.ByGift}
	a.Streams.TotalStreams = len(sessions)
	for _, sess := range sessions {
		a.Streams.TotalPeakViewers += sess.PeakViewers
		if sess.StartedAt != nil && (a.Streams.LastStreamAt == nil || sess.StartedAt.After(*a.Streams.LastStreamAt)) {
			a.Streams.LastStreamAt = sess.StartedAt
		}
	}
	if len(sessions) > 0 {
		a.Streams.AvgPeakViewers = float64(a.Streams.TotalPeakViewers) / float64(len(sessions))
	}

	now := s.now()
	weekAgo := now.Add(-7 * 24 * time.Hour)
	monthAgo := now.Add(-30 * 24 * time.Hour)
	a.Followers.TotalFollowers = len(followers)
	for _, f := range followers {
		if f.FollowedAt.After(weekAgo) {
			a.Followers.NewLast7Days++
		}
		if f.FollowedAt.After(monthAgo) {
			a.Followers.NewLast30Days++
		}
	}

	return a, nil
}
