// Package analyticssvc implements the analytics dashboards (roadmap Phase
// 17). Every dependency on auth/ledger/streaming/admin is injected as a
// closure, matching the DI convention used throughout this codebase.
package analyticssvc

import (
	"context"
	"time"

	"github.com/lumena/analytics"
)

// ─── Injected closures ──────────────────────────────────────────────────────

// RequirePermissionFunc adapts adminsvc.Service.HasPermission(ctx, actorID,
// admin.PermAnalyticsView) — analytics never imports admin directly.
type RequirePermissionFunc func(ctx context.Context, actorID string) (bool, error)

type GiftTxn struct {
	SenderID        string
	RecipientID     string
	GiftID          string
	CreatorDiamonds int64
	GrossCoins      int64
	CreatedAt       time.Time
}

type PurchaseTxn struct {
	AccountID string
	Coins     int64
	CreatedAt time.Time
}

type BroadcastSession struct {
	HostID    string
	StartedAt *time.Time
}

type ListGiftsFunc func(ctx context.Context, since time.Time) ([]GiftTxn, error)
type ListPurchasesFunc func(ctx context.Context, since time.Time) ([]PurchaseTxn, error)
type ListBroadcastsFunc func(ctx context.Context) ([]BroadcastSession, error)

// ─── Repo ───────────────────────────────────────────────────────────────────

type EventRepo interface {
	Append(ctx context.Context, accountID string, eventType analytics.EventType) (*analytics.Event, error)
	ListSince(ctx context.Context, since time.Time) ([]analytics.Event, error)
	ListByType(ctx context.Context, eventType analytics.EventType) ([]analytics.Event, error)
}

// ─── Service ────────────────────────────────────────────────────────────────

type Service struct {
	events EventRepo

	requirePermission RequirePermissionFunc
	listGifts         ListGiftsFunc
	listPurchases     ListPurchasesFunc
	listBroadcasts    ListBroadcastsFunc

	now func() time.Time
}

func New(events EventRepo, requirePermission RequirePermissionFunc, listGifts ListGiftsFunc, listPurchases ListPurchasesFunc, listBroadcasts ListBroadcastsFunc) *Service {
	return &Service{
		events: events, requirePermission: requirePermission,
		listGifts: listGifts, listPurchases: listPurchases, listBroadcasts: listBroadcasts,
		now: time.Now,
	}
}

func (s *Service) requirePerm(ctx context.Context, actorID string) error {
	ok, err := s.requirePermission(ctx, actorID)
	if err != nil {
		return err
	}
	if !ok {
		return analytics.ErrForbidden
	}
	return nil
}

// RecordEvent is the ingestion side of the pipeline — called by authsvc's
// AuthEventHook. No permission check: this is internal, not an HTTP
// endpoint any caller can hit directly.
func (s *Service) RecordEvent(ctx context.Context, accountID string, eventType analytics.EventType) {
	_, _ = s.events.Append(ctx, accountID, eventType)
}

// ─── DAU / MAU ──────────────────────────────────────────────────────────────

func (s *Service) GetDAUMAU(ctx context.Context, actorID string) (*analytics.DAUMAU, error) {
	if err := s.requirePerm(ctx, actorID); err != nil {
		return nil, err
	}
	now := s.now()
	monthEvents, err := s.events.ListSince(ctx, now.Add(-30*24*time.Hour))
	if err != nil {
		return nil, err
	}
	dayCutoff := now.Add(-24 * time.Hour)
	dau := map[string]bool{}
	mau := map[string]bool{}
	for _, e := range monthEvents {
		mau[e.AccountID] = true
		if e.CreatedAt.After(dayCutoff) {
			dau[e.AccountID] = true
		}
	}
	return &analytics.DAUMAU{DAU: len(dau), MAU: len(mau), AsOf: now}, nil
}

// ─── Retention ──────────────────────────────────────────────────────────────

// GetRetention computes, for the cohort of accounts created on cohortDate,
// what fraction returned (had any event) exactly N days later, for
// N in {1, 7, 30}.
func (s *Service) GetRetention(ctx context.Context, actorID string, cohortDate time.Time) ([]analytics.RetentionPoint, error) {
	if err := s.requirePerm(ctx, actorID); err != nil {
		return nil, err
	}
	dayStart := time.Date(cohortDate.Year(), cohortDate.Month(), cohortDate.Day(), 0, 0, 0, 0, cohortDate.Location())
	dayEnd := dayStart.Add(24 * time.Hour)

	created, err := s.events.ListByType(ctx, analytics.EventAccountCreated)
	if err != nil {
		return nil, err
	}
	cohort := map[string]bool{}
	for _, e := range created {
		if !e.CreatedAt.Before(dayStart) && e.CreatedAt.Before(dayEnd) {
			cohort[e.AccountID] = true
		}
	}

	allEvents, err := s.events.ListSince(ctx, dayStart)
	if err != nil {
		return nil, err
	}

	var points []analytics.RetentionPoint
	for _, offset := range []int{1, 7, 30} {
		winStart := dayStart.Add(time.Duration(offset) * 24 * time.Hour)
		winEnd := winStart.Add(24 * time.Hour)
		retained := map[string]bool{}
		for _, e := range allEvents {
			if cohort[e.AccountID] && !e.CreatedAt.Before(winStart) && e.CreatedAt.Before(winEnd) {
				retained[e.AccountID] = true
			}
		}
		pct := 0.0
		if len(cohort) > 0 {
			pct = float64(len(retained)) / float64(len(cohort)) * 100
		}
		points = append(points, analytics.RetentionPoint{
			DaysAfter: offset, CohortSize: len(cohort), RetainedCount: len(retained), RetainedPct: pct,
		})
	}
	return points, nil
}

// ─── Funnel ─────────────────────────────────────────────────────────────────

// GetFunnel is doc 12 Phase 17's "onboarding, first gift, first broadcast"
// — each step a running count of distinct accounts that have ever reached
// it, read live from auth/ledger/streaming rather than a separate copy.
func (s *Service) GetFunnel(ctx context.Context, actorID string) ([]analytics.FunnelStep, error) {
	if err := s.requirePerm(ctx, actorID); err != nil {
		return nil, err
	}
	registered, err := s.events.ListByType(ctx, analytics.EventAccountCreated)
	if err != nil {
		return nil, err
	}
	loggedIn, err := s.events.ListByType(ctx, analytics.EventLogin)
	if err != nil {
		return nil, err
	}
	gifts, err := s.listGifts(ctx, time.Time{})
	if err != nil {
		return nil, err
	}
	broadcasts, err := s.listBroadcasts(ctx)
	if err != nil {
		return nil, err
	}

	registeredSet := distinctAccounts(registered)
	loggedInSet := map[string]bool{}
	for _, e := range loggedIn {
		loggedInSet[e.AccountID] = true
	}
	for id := range registeredSet {
		loggedInSet[id] = true // registration itself issues a session
	}
	giftSenders := map[string]bool{}
	for _, g := range gifts {
		giftSenders[g.SenderID] = true
	}
	broadcastHosts := map[string]bool{}
	for _, b := range broadcasts {
		broadcastHosts[b.HostID] = true
	}

	base := float64(len(registeredSet))
	pct := func(n int) float64 {
		if base == 0 {
			return 0
		}
		return float64(n) / base * 100
	}
	return []analytics.FunnelStep{
		{Name: "registered", Count: len(registeredSet), PctOfFirst: pct(len(registeredSet))},
		{Name: "logged_in", Count: len(loggedInSet), PctOfFirst: pct(len(loggedInSet))},
		{Name: "first_gift_sent", Count: len(giftSenders), PctOfFirst: pct(len(giftSenders))},
		{Name: "first_broadcast_started", Count: len(broadcastHosts), PctOfFirst: pct(len(broadcastHosts))},
	}, nil
}

func distinctAccounts(events []analytics.Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		out[e.AccountID] = true
	}
	return out
}

// ─── Economy summary ─────────────────────────────────────────────────────

func (s *Service) GetEconomySummary(ctx context.Context, actorID string, days int) (*analytics.EconomySummary, error) {
	if err := s.requirePerm(ctx, actorID); err != nil {
		return nil, err
	}
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	gifts, err := s.listGifts(ctx, since)
	if err != nil {
		return nil, err
	}
	purchases, err := s.listPurchases(ctx, since)
	if err != nil {
		return nil, err
	}
	summary := &analytics.EconomySummary{RangeDays: days, TotalGiftsSent: len(gifts), TotalPurchases: len(purchases)}
	for _, g := range gifts {
		summary.TotalCoinsSpent += g.GrossCoins
		summary.TotalDiamondsIssued += g.CreatorDiamonds
	}
	for _, p := range purchases {
		summary.TotalCoinsPurchased += p.Coins
	}
	return summary, nil
}

// ─── Creator trend ─────────────────────────────────────────────────────────

// GetCreatorTrend buckets a single creator's gifts-received and
// broadcast-start counts by day over the last `days` days (doc 07 §12's
// GET /creator/analytics?range=). A creator can always see their own
// trend — the analytics.view permission is only required to view someone
// else's (the admin/finance case).
func (s *Service) GetCreatorTrend(ctx context.Context, actorID, creatorID string, days int) ([]analytics.CreatorTrendPoint, error) {
	if actorID != creatorID {
		if err := s.requirePerm(ctx, actorID); err != nil {
			return nil, err
		}
	}
	since := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	gifts, err := s.listGifts(ctx, since)
	if err != nil {
		return nil, err
	}
	broadcasts, err := s.listBroadcasts(ctx)
	if err != nil {
		return nil, err
	}

	buckets := map[string]*analytics.CreatorTrendPoint{}
	bucket := func(date string) *analytics.CreatorTrendPoint {
		p, ok := buckets[date]
		if !ok {
			p = &analytics.CreatorTrendPoint{Date: date}
			buckets[date] = p
		}
		return p
	}

	for _, g := range gifts {
		if g.RecipientID != creatorID {
			continue
		}
		p := bucket(g.CreatedAt.Format("2006-01-02"))
		p.GiftsReceived++
		p.DiamondsEarned += g.CreatorDiamonds
	}
	for _, b := range broadcasts {
		if b.HostID != creatorID || b.StartedAt == nil || b.StartedAt.Before(since) {
			continue
		}
		p := bucket(b.StartedAt.Format("2006-01-02"))
		p.BroadcastCount++
	}

	out := make([]analytics.CreatorTrendPoint, 0, len(buckets))
	for _, p := range buckets {
		out = append(out, *p)
	}
	sortTrendByDate(out)
	return out, nil
}

func sortTrendByDate(points []analytics.CreatorTrendPoint) {
	for i := 1; i < len(points); i++ {
		for j := i; j > 0 && points[j].Date < points[j-1].Date; j-- {
			points[j], points[j-1] = points[j-1], points[j]
		}
	}
}
