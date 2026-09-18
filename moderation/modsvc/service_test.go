package modsvc_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lumena/moderation"
	"github.com/lumena/moderation/modsvc"
)

type fakeAuth struct {
	status map[string]string
}

func newFakeAuth() *fakeAuth { return &fakeAuth{status: map[string]string{}} }

func (f *fakeAuth) apply(_ context.Context, accountID string, action moderation.EnforcementAction) error {
	switch action {
	case moderation.ActionRestrictBroadcast, moderation.ActionRestrictPV:
		f.status[accountID] = "restricted"
	case moderation.ActionSuspend, moderation.ActionTerminate:
		f.status[accountID] = "suspended"
	}
	return nil
}
func (f *fakeAuth) reinstate(_ context.Context, accountID string) error {
	f.status[accountID] = "active"
	return nil
}

func newTestService() (*modsvc.Service, *fakeAuth) {
	auth := newFakeAuth()
	risk := modsvc.NewRiskEngine(
		moderation.NewMemRiskRepo(),
		func(context.Context, string) (bool, error) { return true, nil },
		func(context.Context, string) (time.Time, error) { return time.Now(), nil },
		func(context.Context, string) (int, error) { return 0, nil },
		func(context.Context, string, string) (bool, error) { return false, nil },
	)
	svc := modsvc.New(
		moderation.NewMemReportRepo(),
		moderation.NewMemEnforcementRepo(),
		moderation.NewMemAppealRepo(),
		moderation.NewMemModeratorRepo(),
		risk,
		auth.apply,
		auth.reinstate,
	)
	return svc, auth
}

func TestSubmitReport_RateLimitedPerReporterSubject24h(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()

	_, err := svc.SubmitReport(ctx, "alice", "user", "bob", "harassment", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.SubmitReport(ctx, "alice", "user", "bob", "harassment", "", "")
	if !errors.Is(err, moderation.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited on second report of same subject within 24h, got %v", err)
	}
	// Different subject is fine.
	_, err = svc.SubmitReport(ctx, "alice", "user", "carol", "harassment", "", "")
	if err != nil {
		t.Fatalf("expected report against a different subject to succeed, got %v", err)
	}
}

func TestListQueue_RequiresModerator(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	_, err := svc.SubmitReport(ctx, "alice", "user", "bob", "spam", "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ListQueue(ctx, "alice", "")
	if !errors.Is(err, moderation.ErrNotModerator) {
		t.Fatalf("expected ErrNotModerator, got %v", err)
	}

	if err := svc.DevGrantModerator(ctx, "mod1"); err != nil {
		t.Fatal(err)
	}
	items, err := svc.ListQueue(ctx, "mod1", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 queued report, got %d", len(items))
	}
}

func TestCreateEnforcement_UnknownRuleRejected(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	_, err := svc.CreateEnforcement(ctx, "human:mod1", "bob", "NOT-A-RULE", moderation.ActionWarn, 0, "evidence-1")
	if !errors.Is(err, moderation.ErrRuleNotFound) {
		t.Fatalf("expected ErrRuleNotFound, got %v", err)
	}
}

func TestCreateEnforcement_AutomatedTerminationForbidden(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	_, err := svc.CreateEnforcement(ctx, "auto:classifier", "bob", "CS-01", moderation.ActionTerminate, 0, "session:s1")
	if !errors.Is(err, moderation.ErrAutomatedTerminationForbidden) {
		t.Fatalf("expected ErrAutomatedTerminationForbidden, got %v", err)
	}
}

func TestCreateEnforcement_HumanTerminationAllowed(t *testing.T) {
	ctx := context.Background()
	svc, auth := newTestService()
	e, err := svc.CreateEnforcement(ctx, "human:mod1", "bob", "CS-01", moderation.ActionTerminate, 0, "session:s1")
	if err != nil {
		t.Fatal(err)
	}
	if e.DecidedBy != "human:mod1" {
		t.Fatalf("expected decided_by human:mod1, got %s", e.DecidedBy)
	}
	if auth.status["bob"] != "suspended" {
		t.Fatalf("expected bob's account effect applied, got %v", auth.status)
	}
}

func TestCreateEnforcement_AutomatedNonTerminationAllowed(t *testing.T) {
	ctx := context.Background()
	svc, auth := newTestService()
	e, err := svc.CreateEnforcement(ctx, "auto:classifier", "bob", "CS-01", moderation.ActionRestrictBroadcast, 0, "session:s1")
	if err != nil {
		t.Fatal(err)
	}
	if !e.Appealable {
		t.Fatal("expected non-warn enforcement to be appealable")
	}
	if auth.status["bob"] != "restricted" {
		t.Fatalf("expected bob restricted, got %v", auth.status)
	}
}

func TestFileAppeal_OverturnReinstatesAccount(t *testing.T) {
	ctx := context.Background()
	svc, auth := newTestService()
	if err := svc.DevGrantModerator(ctx, "mod1"); err != nil {
		t.Fatal(err)
	}
	e, err := svc.CreateEnforcement(ctx, "human:mod1", "bob", "HR-01", moderation.ActionSuspend, 0, "evidence-1")
	if err != nil {
		t.Fatal(err)
	}
	if auth.status["bob"] != "suspended" {
		t.Fatalf("expected bob suspended, got %v", auth.status)
	}

	appeal, err := svc.FileAppeal(ctx, "bob", e.ID, "it wasn't me")
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate pending appeal rejected.
	_, err = svc.FileAppeal(ctx, "bob", e.ID, "again")
	if !errors.Is(err, moderation.ErrAlreadyAppealed) {
		t.Fatalf("expected ErrAlreadyAppealed, got %v", err)
	}

	decided, err := svc.DecideAppeal(ctx, "mod1", appeal.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if decided.State != moderation.AppealOverturned {
		t.Fatalf("expected overturned, got %s", decided.State)
	}
	if auth.status["bob"] != "active" {
		t.Fatalf("expected bob reinstated to active, got %v", auth.status)
	}
}

// TestConcurrentDecideAppeal_OnlyOneDecisionEverSticks is a regression test
// for a real race: MemAppealRepo.Decide used to unconditionally overwrite
// an appeal's state with no check that it was still Pending. Two
// concurrent DecideAppeal calls (e.g. a double click, or two moderators)
// could both succeed — and since DecideAppeal only calls the external
// reinstate hook when its own call reports "overturned", an uphold and an
// overturn racing on the same appeal could both fire, leaving the account
// reinstated while the persisted appeal shows Upheld (enforcement
// supposedly still in effect) or vice versa. This races uphold against
// overturn many times and asserts the invariant that must always hold: the
// account's final status agrees with whichever decision actually won.
func TestConcurrentDecideAppeal_OnlyOneDecisionEverSticks(t *testing.T) {
	for i := 0; i < 50; i++ {
		ctx := context.Background()
		svc, auth := newTestService()
		if err := svc.DevGrantModerator(ctx, "mod1"); err != nil {
			t.Fatal(err)
		}
		if err := svc.DevGrantModerator(ctx, "mod2"); err != nil {
			t.Fatal(err)
		}
		e, err := svc.CreateEnforcement(ctx, "human:mod1", "bob", "HR-01", moderation.ActionSuspend, 0, "evidence-1")
		if err != nil {
			t.Fatal(err)
		}
		appeal, err := svc.FileAppeal(ctx, "bob", e.ID, "it wasn't me")
		if err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		results := make([]error, 2)
		wg.Add(2)
		go func() { defer wg.Done(); _, results[0] = svc.DecideAppeal(ctx, "mod1", appeal.ID, true) }()  // uphold
		go func() { defer wg.Done(); _, results[1] = svc.DecideAppeal(ctx, "mod2", appeal.ID, false) }() // overturn
		wg.Wait()

		// Exactly one of the two must have won; the other must be rejected
		// as already-decided, never silently ignored or both-applied.
		succeeded := 0
		for _, err := range results {
			if err == nil {
				succeeded++
			} else if !errors.Is(err, moderation.ErrAppealAlreadyDecided) {
				t.Fatalf("iteration %d: unexpected error %v", i, err)
			}
		}
		if succeeded != 1 {
			t.Fatalf("iteration %d: expected exactly one DecideAppeal call to succeed, got %d", i, succeeded)
		}

		// The account's actual status must agree with whichever decision won.
		overturnWon := results[1] == nil
		wantStatus := "suspended"
		if overturnWon {
			wantStatus = "active"
		}
		if auth.status["bob"] != wantStatus {
			t.Fatalf("iteration %d: overturnWon=%v but bob's status is %q (want %q)", i, overturnWon, auth.status["bob"], wantStatus)
		}
	}
}

func TestFileAppeal_WarnIsNotAppealable(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	e, err := svc.CreateEnforcement(ctx, "human:mod1", "bob", "SP-01", moderation.ActionWarn, 0, "evidence-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.FileAppeal(ctx, "bob", e.ID, "statement")
	if !errors.Is(err, moderation.ErrNotAppealable) {
		t.Fatalf("expected ErrNotAppealable, got %v", err)
	}
}

func TestFileAppeal_OnlyOwnerCanFile(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService()
	e, err := svc.CreateEnforcement(ctx, "human:mod1", "bob", "SP-01", moderation.ActionMute, 0, "evidence-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.FileAppeal(ctx, "eve", e.ID, "statement")
	if !errors.Is(err, moderation.ErrNotOwner) {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
}

func TestRiskEngine_MultiLowFollowerContactFlagsForReview(t *testing.T) {
	ctx := context.Background()
	riskRepo := moderation.NewMemRiskRepo()
	risk := modsvc.NewRiskEngine(
		riskRepo,
		func(context.Context, string) (bool, error) { return true, nil }, // sender is age-assured
		func(context.Context, string) (time.Time, error) { return time.Now().Add(-1 * time.Hour), nil }, // recipients just registered
		func(context.Context, string) (int, error) { return 2, nil }, // low follower count
		func(context.Context, string, string) (bool, error) { return false, nil },
	)
	svc := modsvc.New(
		moderation.NewMemReportRepo(), moderation.NewMemEnforcementRepo(), moderation.NewMemAppealRepo(),
		moderation.NewMemModeratorRepo(), risk, nil, nil,
	)
	if err := svc.DevGrantModerator(ctx, "mod1"); err != nil {
		t.Fatal(err)
	}

	svc.RecordMessage(ctx, "predator", "newbie1", "hey there")
	svc.RecordMessage(ctx, "predator", "newbie2", "hi")
	svc.RecordMessage(ctx, "predator", "newbie3", "hello")

	profile, err := svc.GetRiskProfile(ctx, "mod1", "predator")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ReviewStatus == moderation.ReviewNone {
		t.Fatalf("expected review flag after contacting 3 low-follower new accounts, got %+v", profile)
	}
	found := false
	for _, r := range profile.RiskReasons {
		if r == "multi_low_follower_contact" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected multi_low_follower_contact reason, got %v", profile.RiskReasons)
	}

	queue, err := svc.ListRiskQueue(ctx, "mod1")
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 || queue[0].AccountID != "predator" {
		t.Fatalf("expected predator in risk queue, got %+v", queue)
	}
}

func TestRiskEngine_RapidEscalationToPV(t *testing.T) {
	ctx := context.Background()
	riskRepo := moderation.NewMemRiskRepo()
	risk := modsvc.NewRiskEngine(
		riskRepo,
		func(context.Context, string) (bool, error) { return false, nil }, // not adult-flagged, so multi-contact signal won't fire
		func(context.Context, string) (time.Time, error) { return time.Now(), nil },
		func(context.Context, string) (int, error) { return 100, nil },
		func(context.Context, string, string) (bool, error) { return false, nil }, // no reciprocal follow
	)
	svc := modsvc.New(
		moderation.NewMemReportRepo(), moderation.NewMemEnforcementRepo(), moderation.NewMemAppealRepo(),
		moderation.NewMemModeratorRepo(), risk, nil, nil,
	)
	if err := svc.DevGrantModerator(ctx, "mod1"); err != nil {
		t.Fatal(err)
	}

	svc.RecordMessage(ctx, "alice", "bob", "hi")
	svc.RecordPVRequest(ctx, "alice", "bob")

	profile, err := svc.GetRiskProfile(ctx, "mod1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]bool{}
	for _, r := range profile.RiskReasons {
		reasons[r] = true
	}
	if !reasons["rapid_escalation_to_pv"] {
		t.Fatalf("expected rapid_escalation_to_pv, got %v", profile.RiskReasons)
	}
	if !reasons["no_reciprocal_engagement"] {
		t.Fatalf("expected no_reciprocal_engagement, got %v", profile.RiskReasons)
	}
}

func TestRiskEngine_NeverCreatesEnforcement(t *testing.T) {
	// "Not an automated ban" — regardless of how high the score climbs, the
	// risk engine has no path to modsvc.Service.CreateEnforcement.
	ctx := context.Background()
	riskRepo := moderation.NewMemRiskRepo()
	risk := modsvc.NewRiskEngine(
		riskRepo,
		func(context.Context, string) (bool, error) { return true, nil },
		func(context.Context, string) (time.Time, error) { return time.Now(), nil },
		func(context.Context, string) (int, error) { return 0, nil },
		func(context.Context, string, string) (bool, error) { return false, nil },
	)
	enforcements := moderation.NewMemEnforcementRepo()
	svc := modsvc.New(
		moderation.NewMemReportRepo(), enforcements, moderation.NewMemAppealRepo(),
		moderation.NewMemModeratorRepo(), risk, nil, nil,
	)
	for i := 0; i < 10; i++ {
		svc.RecordMessage(ctx, "predator", "newbie", "meet in person, our secret")
		svc.RecordPVRequest(ctx, "predator", "newbie")
	}
	items, err := enforcements.ListByAccount(ctx, "predator")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected zero enforcements from risk signals alone, got %d", len(items))
	}
}
