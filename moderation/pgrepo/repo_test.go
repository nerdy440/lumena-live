package pgrepo_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/lumena/db"
	"github.com/lumena/moderation"
	"github.com/lumena/moderation/pgrepo"
)

func newTestRepos(t *testing.T) (*pgrepo.ReportRepo, *pgrepo.EnforcementRepo, *pgrepo.AppealRepo, *pgrepo.RiskRepo, *pgrepo.ModeratorRepo) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	return pgrepo.NewReportRepo(pool), pgrepo.NewEnforcementRepo(pool), pgrepo.NewAppealRepo(pool), pgrepo.NewRiskRepo(pool), pgrepo.NewModeratorRepo(pool)
}

func TestReport_CreateGetRateLimitAndList(t *testing.T) {
	reports, _, _, _, _ := newTestRepos(t)
	ctx := context.Background()

	recent, err := reports.RecentlyReported(ctx, "alice", "bob", 24*time.Hour)
	if err != nil || recent {
		t.Fatalf("should not be rate-limited yet: %v err=%v", recent, err)
	}

	rep, err := reports.Create(ctx, moderation.Report{
		ReporterID: "alice", SubjectType: "user", SubjectID: "bob",
		ReasonCode: "harassment", State: moderation.ReportOpen, CreatedAt: time.Now(),
	})
	if err != nil || rep.ID == "" || rep.CaseID == "" {
		t.Fatalf("create: %+v err=%v", rep, err)
	}

	recent2, err := reports.RecentlyReported(ctx, "alice", "bob", 24*time.Hour)
	if err != nil || !recent2 {
		t.Fatalf("should now be rate-limited: %v err=%v", recent2, err)
	}

	if err := reports.UpdateState(ctx, rep.ID, moderation.ReportTriaged); err != nil {
		t.Fatalf("update state: %v", err)
	}
	got, err := reports.Get(ctx, rep.ID)
	if err != nil || got.State != moderation.ReportTriaged {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	open, err := reports.List(ctx, moderation.ReportOpen)
	if err != nil || len(open) != 0 {
		t.Fatalf("expected no open reports (it's now triaged), got %+v err=%v", open, err)
	}
	triaged, err := reports.List(ctx, moderation.ReportTriaged)
	if err != nil || len(triaged) != 1 {
		t.Fatalf("expected 1 triaged report, got %+v err=%v", triaged, err)
	}
}

func TestEnforcement_CreateGetListByAccount(t *testing.T) {
	_, enforcements, _, _, _ := newTestRepos(t)
	ctx := context.Background()

	e, err := enforcements.Create(ctx, moderation.Enforcement{
		AccountID: "acc-1", RuleID: "rule-1", Action: moderation.ActionWarn,
		DecidedBy: "human:mod-1", Appealable: true,
	})
	if err != nil || e.ID == "" || e.CaseID == "" {
		t.Fatalf("create: %+v err=%v", e, err)
	}

	got, err := enforcements.Get(ctx, e.ID)
	if err != nil || got.Action != moderation.ActionWarn {
		t.Fatalf("get: %+v err=%v", got, err)
	}

	list, err := enforcements.ListByAccount(ctx, "acc-1")
	if err != nil || len(list) != 1 {
		t.Fatalf("list by account: %+v err=%v", list, err)
	}
}

func TestAppeal_OnePendingAtATimeAndDecideIsAtomic(t *testing.T) {
	_, enforcements, appeals, _, _ := newTestRepos(t)
	ctx := context.Background()
	e, _ := enforcements.Create(ctx, moderation.Enforcement{AccountID: "acc-1", RuleID: "rule-1", Action: moderation.ActionSuspend, Appealable: true})

	pending, err := appeals.HasPending(ctx, e.ID)
	if err != nil || pending {
		t.Fatalf("should have no pending appeal yet: %v err=%v", pending, err)
	}

	a, err := appeals.Create(ctx, moderation.Appeal{EnforcementID: e.ID, AccountID: "acc-1", Statement: "it wasn't me", State: moderation.AppealPending})
	if err != nil {
		t.Fatalf("create appeal: %v", err)
	}

	pending2, err := appeals.HasPending(ctx, e.ID)
	if err != nil || !pending2 {
		t.Fatalf("should now have a pending appeal: %v err=%v", pending2, err)
	}

	decided, err := appeals.Decide(ctx, a.ID, moderation.AppealOverturned, "human:mod-2")
	if err != nil || decided.State != moderation.AppealOverturned || decided.DecidedAt == nil {
		t.Fatalf("decide: %+v err=%v", decided, err)
	}

	// A second decision on the same (already-decided) appeal must fail.
	_, err = appeals.Decide(ctx, a.ID, moderation.AppealUpheld, "human:mod-3")
	if !errors.Is(err, moderation.ErrAppealAlreadyDecided) {
		t.Fatalf("expected ErrAppealAlreadyDecided, got %v", err)
	}
}

func TestRisk_DefaultThenUpsertThenListFlagged(t *testing.T) {
	_, _, _, risk, _ := newTestRepos(t)
	ctx := context.Background()

	def, err := risk.Get(ctx, "acc-1")
	if err != nil || def.ReviewStatus != moderation.ReviewNone {
		t.Fatalf("default: %+v err=%v", def, err)
	}

	if err := risk.Upsert(ctx, moderation.RiskProfile{
		AccountID: "acc-1", RiskScore: 7.5, RiskReasons: []string{"rapid_pv_requests"},
		ReviewStatus: moderation.ReviewManualReview,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	_ = risk.Upsert(ctx, moderation.RiskProfile{AccountID: "acc-2", RiskScore: 1, ReviewStatus: moderation.ReviewNone})

	got, err := risk.Get(ctx, "acc-1")
	if err != nil || got.RiskScore != 7.5 || len(got.RiskReasons) != 1 {
		t.Fatalf("get after upsert: %+v err=%v", got, err)
	}

	flagged, err := risk.ListFlagged(ctx)
	if err != nil || len(flagged) != 1 || flagged[0].AccountID != "acc-1" {
		t.Fatalf("list flagged: %+v err=%v", flagged, err)
	}
}

func TestModerator_GrantThenIsModerator(t *testing.T) {
	_, _, _, _, moderators := newTestRepos(t)
	ctx := context.Background()

	is, err := moderators.IsModerator(ctx, "acc-1")
	if err != nil || is {
		t.Fatalf("should not be a moderator yet: %v err=%v", is, err)
	}
	if err := moderators.Grant(ctx, "acc-1"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	is2, err := moderators.IsModerator(ctx, "acc-1")
	if err != nil || !is2 {
		t.Fatalf("should now be a moderator: %v err=%v", is2, err)
	}
}
