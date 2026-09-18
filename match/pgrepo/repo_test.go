package pgrepo_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lumena/db"
	"github.com/lumena/match"
	"github.com/lumena/match/pgrepo"
)

func newTestRepo(t *testing.T) *pgrepo.Repo {
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
	return pgrepo.New(pool)
}

func TestCreateAndGetRequest(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()

	req, err := r.CreateRequest(ctx, "alice", match.Preferences{GenderPreference: "female", MinAge: 21, MaxAge: 30})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if req.State != match.StateSearching || req.Preferences.MinAge != 21 {
		t.Fatalf("unexpected request: %+v", req)
	}

	got, err := r.GetRequest(ctx, req.ID)
	if err != nil || got.ID != req.ID {
		t.Fatalf("get: %+v err=%v", got, err)
	}
}

func TestUpdateRequest_ResolvedAtAndMatchedWith(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	req, _ := r.CreateRequest(ctx, "alice", match.Preferences{})

	req.State = match.StateMatched
	req.MatchedWith = "bob"
	req.ResolvedAt = time.Now().Truncate(time.Millisecond)
	if err := r.UpdateRequest(ctx, req); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := r.GetRequest(ctx, req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.State != match.StateMatched || got.MatchedWith != "bob" || got.ResolvedAt.IsZero() {
		t.Fatalf("update did not persist: %+v", got)
	}
}

func TestSearchingRequests_ExcludesSelfAndNonSearching(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	reqAlice, _ := r.CreateRequest(ctx, "alice", match.Preferences{})
	_, _ = r.CreateRequest(ctx, "bob", match.Preferences{})
	reqCarol, _ := r.CreateRequest(ctx, "carol", match.Preferences{})
	reqCarol.State = match.StateCancelled
	_ = r.UpdateRequest(ctx, reqCarol)

	pool, err := r.SearchingRequests(ctx, reqAlice.AccountID)
	if err != nil {
		t.Fatalf("searching: %v", err)
	}
	if len(pool) != 1 || pool[0].AccountID != "bob" {
		t.Fatalf("expected only bob in the pool, got %+v", pool)
	}
}

func TestCandidateLifecycle_AddGetPendingDecide(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	req, _ := r.CreateRequest(ctx, "alice", match.Preferences{})

	c := &match.Candidate{
		RequestID: req.ID, CandidateID: "bob", Score: 0.9,
		ScoreReasons: map[string]any{"shared_language": true}, OfferedAt: time.Now(),
	}
	if err := r.AddCandidate(ctx, c); err != nil {
		t.Fatalf("add candidate: %v", err)
	}

	pending, err := r.GetPendingCandidate(ctx, req.ID)
	if err != nil || pending == nil || pending.CandidateID != "bob" {
		t.Fatalf("pending: %+v err=%v", pending, err)
	}
	if pending.ScoreReasons["shared_language"] != true {
		t.Fatalf("score reasons not round-tripped: %+v", pending.ScoreReasons)
	}

	if err := r.SetCandidateDecision(ctx, req.ID, "bob", match.DecisionConnect); err != nil {
		t.Fatalf("decide: %v", err)
	}
	// Once decided, it's no longer "pending".
	pending2, err := r.GetPendingCandidate(ctx, req.ID)
	if err != nil || pending2 != nil {
		t.Fatalf("expected no pending candidate after decision, got %+v err=%v", pending2, err)
	}

	got, err := r.GetCandidate(ctx, req.ID, "bob")
	if err != nil || got.Decision != match.DecisionConnect {
		t.Fatalf("get candidate: %+v err=%v", got, err)
	}
}

func TestFindMirror_BothSidesOfferedEachOther(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	reqAlice, _ := r.CreateRequest(ctx, "alice", match.Preferences{})
	reqBob, _ := r.CreateRequest(ctx, "bob", match.Preferences{})

	_ = r.AddCandidate(ctx, &match.Candidate{RequestID: reqAlice.ID, CandidateID: "bob", OfferedAt: time.Now()})
	_ = r.AddCandidate(ctx, &match.Candidate{RequestID: reqBob.ID, CandidateID: "alice", OfferedAt: time.Now()})

	mirror, err := r.FindMirror(ctx, reqAlice.ID, "bob")
	if err != nil || mirror == nil {
		t.Fatalf("expected a mirror candidate, got %+v err=%v", mirror, err)
	}
	if mirror.RequestID != reqBob.ID || mirror.CandidateID != "alice" {
		t.Fatalf("unexpected mirror: %+v", mirror)
	}
}

func TestExclusion_SymmetricAndExpires(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	now := time.Now()

	if err := r.AddExclusion(ctx, match.Exclusion{AccountID: "alice", ExcludedID: "bob", Reason: match.ExclusionBlocked}); err != nil {
		t.Fatalf("add exclusion: %v", err)
	}
	excluded, err := r.IsExcluded(ctx, "bob", "alice", now) // reversed order
	if err != nil || !excluded {
		t.Fatalf("expected symmetric exclusion, got %v err=%v", excluded, err)
	}

	if err := r.AddExclusion(ctx, match.Exclusion{
		AccountID: "carol", ExcludedID: "dave", Reason: match.ExclusionSkippedRecent,
		ExpiresAt: now.Add(-time.Hour), // already expired
	}); err != nil {
		t.Fatalf("add expiring exclusion: %v", err)
	}
	stillExcluded, err := r.IsExcluded(ctx, "carol", "dave", now)
	if err != nil || stillExcluded {
		t.Fatalf("expired exclusion should no longer apply, got %v err=%v", stillExcluded, err)
	}
}

func TestSkipCount_CountsDistinctSkippers(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	since := time.Now().Add(-time.Hour)

	reqA, _ := r.CreateRequest(ctx, "alice", match.Preferences{})
	reqB, _ := r.CreateRequest(ctx, "bob", match.Preferences{})
	_ = r.AddCandidate(ctx, &match.Candidate{RequestID: reqA.ID, CandidateID: "zara", OfferedAt: time.Now(), Decision: match.DecisionSkip})
	_ = r.AddCandidate(ctx, &match.Candidate{RequestID: reqB.ID, CandidateID: "zara", OfferedAt: time.Now(), Decision: match.DecisionSkip})

	n, err := r.SkipCount(ctx, "zara", since)
	if err != nil || n != 2 {
		t.Fatalf("skip count = %d, want 2 (err=%v)", n, err)
	}
}

func TestPreferences_DefaultThenSet(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if err := db.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	prefs := pgrepo.NewPreferencesRepo(pool)

	def, err := prefs.Get(ctx, "alice")
	if err != nil || def != (match.Preferences{}) {
		t.Fatalf("expected zero-value preferences, got %+v err=%v", def, err)
	}

	if err := prefs.Set(ctx, "alice", match.Preferences{GenderPreference: "female", MinAge: 25, MaxAge: 35}); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := prefs.Get(ctx, "alice")
	if err != nil || got.MinAge != 25 || got.MaxAge != 35 || got.GenderPreference != "female" {
		t.Fatalf("get after set: %+v err=%v", got, err)
	}
}

func TestRecentRequestCount(t *testing.T) {
	r := newTestRepo(t)
	ctx := context.Background()
	_, _ = r.CreateRequest(ctx, "alice", match.Preferences{})
	_, _ = r.CreateRequest(ctx, "alice", match.Preferences{})

	n, err := r.RecentRequestCount(ctx, "alice", time.Now().Add(-time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("recent count = %d, want 2 (err=%v)", n, err)
	}
}
