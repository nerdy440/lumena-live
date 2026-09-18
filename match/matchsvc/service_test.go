package matchsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lumena/match"
	"github.com/lumena/match/matchsvc"
)

func alwaysMatchable(context.Context, string) (bool, error) { return true, nil }
func neverMatchable(context.Context, string) (bool, error)  { return false, nil }
func alwaysAged(context.Context, string) (bool, error)      { return true, nil }
func neverAged(context.Context, string) (bool, error)       { return false, nil }
func noBlocks(context.Context, string, string) (bool, error) { return false, nil }

func newService() *matchsvc.Service {
	return matchsvc.NewService(match.NewMemRepo(), alwaysMatchable, alwaysAged, noBlocks)
}

func TestCreateRequest_RequiresOptIn(t *testing.T) {
	svc := matchsvc.NewService(match.NewMemRepo(), neverMatchable, alwaysAged, noBlocks)
	_, err := svc.CreateRequest(context.Background(), "alice", match.Preferences{})
	if !errors.Is(err, match.ErrNotMatchable) {
		t.Fatalf("expected ErrNotMatchable, got %v", err)
	}
}

func TestCreateRequest_RequiresAgeAssurance(t *testing.T) {
	svc := matchsvc.NewService(match.NewMemRepo(), alwaysMatchable, neverAged, noBlocks)
	_, err := svc.CreateRequest(context.Background(), "alice", match.Preferences{})
	if !errors.Is(err, match.ErrNotAgeAssured) {
		t.Fatalf("expected ErrNotAgeAssured, got %v", err)
	}
}

func TestCreateRequest_TwoSearchersGetOfferedEachOther(t *testing.T) {
	ctx := context.Background()
	svc := newService()

	alice, err := svc.CreateRequest(ctx, "alice", match.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	if alice.State != match.StateSearching {
		t.Fatalf("expected alice searching alone, got %s", alice.State)
	}

	bob, err := svc.CreateRequest(ctx, "bob", match.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	if bob.State != match.StateSearching {
		t.Fatalf("expected bob still searching (not auto-matched), got %s", bob.State)
	}

	aliceCand, err := svc.GetPendingCandidate(ctx, "alice", alice.ID)
	if err != nil || aliceCand == nil {
		t.Fatalf("expected alice to be offered a candidate, got %v err=%v", aliceCand, err)
	}
	if aliceCand.CandidateID != "bob" {
		t.Fatalf("expected alice offered bob, got %s", aliceCand.CandidateID)
	}
	bobCand, err := svc.GetPendingCandidate(ctx, "bob", bob.ID)
	if err != nil || bobCand == nil || bobCand.CandidateID != "alice" {
		t.Fatalf("expected bob offered alice, got %+v err=%v", bobCand, err)
	}
}

func TestDecide_MutualConnectResolvesBothRequests(t *testing.T) {
	ctx := context.Background()
	svc := newService()
	alice, _ := svc.CreateRequest(ctx, "alice", match.Preferences{})
	bob, _ := svc.CreateRequest(ctx, "bob", match.Preferences{})

	// Alice connects first — should not resolve yet (bob hasn't decided).
	aliceReq, err := svc.Decide(ctx, "alice", alice.ID, "bob", match.DecisionConnect)
	if err != nil {
		t.Fatal(err)
	}
	if aliceReq.State != match.StateSearching {
		t.Fatalf("expected still searching after one-sided connect, got %s", aliceReq.State)
	}

	bobReq, err := svc.Decide(ctx, "bob", bob.ID, "alice", match.DecisionConnect)
	if err != nil {
		t.Fatal(err)
	}
	if bobReq.State != match.StateMatched || bobReq.MatchedWith != "alice" {
		t.Fatalf("expected bob matched with alice, got state=%s matchedWith=%s", bobReq.State, bobReq.MatchedWith)
	}

	// Alice's request should also have resolved to matched as a side effect.
	aliceFinal, err := svc.GetRequest(ctx, "alice", alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if aliceFinal.State != match.StateMatched || aliceFinal.MatchedWith != "bob" {
		t.Fatalf("expected alice's request also resolved to matched with bob, got state=%s matchedWith=%s", aliceFinal.State, aliceFinal.MatchedWith)
	}
}

func TestDecide_SkipExcludesAndReturnsToSearching(t *testing.T) {
	ctx := context.Background()
	svc := newService()
	alice, _ := svc.CreateRequest(ctx, "alice", match.Preferences{})
	svc.CreateRequest(ctx, "bob", match.Preferences{})

	aliceReq, err := svc.Decide(ctx, "alice", alice.ID, "bob", match.DecisionSkip)
	if err != nil {
		t.Fatal(err)
	}
	if aliceReq.State != match.StateSearching {
		t.Fatalf("expected alice still searching after skip, got %s", aliceReq.State)
	}
	cand, _ := svc.GetPendingCandidate(ctx, "alice", alice.ID)
	if cand != nil {
		t.Fatalf("expected no new candidate offered (bob is the only pool member and was just skipped), got %+v", cand)
	}

	// Create a third searcher — alice should be offered carol, never bob again.
	svc.CreateRequest(ctx, "carol", match.Preferences{})
	cand2, err := svc.GetPendingCandidate(ctx, "alice", alice.ID)
	if err != nil || cand2 == nil {
		t.Fatalf("expected alice offered carol, got %v err=%v", cand2, err)
	}
	if cand2.CandidateID != "carol" {
		t.Fatalf("expected carol, not a re-offer of skipped bob, got %s", cand2.CandidateID)
	}
}

func TestTryMatch_BlockedUserNeverOfferedEitherDirection(t *testing.T) {
	ctx := context.Background()
	blocked := func(_ context.Context, a, b string) (bool, error) {
		return (a == "alice" && b == "bob") || (a == "bob" && b == "alice"), nil
	}
	svc := matchsvc.NewService(match.NewMemRepo(), alwaysMatchable, alwaysAged, blocked)

	alice, _ := svc.CreateRequest(ctx, "alice", match.Preferences{})
	bob, _ := svc.CreateRequest(ctx, "bob", match.Preferences{})

	aliceCand, _ := svc.GetPendingCandidate(ctx, "alice", alice.ID)
	if aliceCand != nil {
		t.Fatalf("expected alice never offered blocked bob, got %+v", aliceCand)
	}
	bobCand, _ := svc.GetPendingCandidate(ctx, "bob", bob.ID)
	if bobCand != nil {
		t.Fatalf("expected bob never offered blocked alice (reverse direction), got %+v", bobCand)
	}
}

func TestCreateRequest_RateLimited(t *testing.T) {
	ctx := context.Background()
	repo := match.NewMemRepo()
	svc := matchsvc.NewService(repo, alwaysMatchable, alwaysAged, noBlocks)

	var lastErr error
	for i := 0; i < 15; i++ {
		req, err := svc.CreateRequest(ctx, "alice", match.Preferences{})
		if err == nil {
			svc.CancelRequest(ctx, "alice", req.ID) // so each iteration creates fresh, not reusing a searching one
		}
		lastErr = err
	}
	if !errors.Is(lastErr, match.ErrRateLimited) {
		t.Fatalf("expected rate limiting to kick in after repeated requests, got %v", lastErr)
	}
}

func TestDecide_WrongOwnerRejected(t *testing.T) {
	ctx := context.Background()
	svc := newService()
	alice, _ := svc.CreateRequest(ctx, "alice", match.Preferences{})
	svc.CreateRequest(ctx, "bob", match.Preferences{})

	_, err := svc.Decide(ctx, "mallory", alice.ID, "bob", match.DecisionConnect)
	if !errors.Is(err, match.ErrNotOwner) {
		t.Fatalf("expected ErrNotOwner, got %v", err)
	}
}

func TestCandidate_ScoreReasonsNeverSerialized(t *testing.T) {
	ctx := context.Background()
	svc := newService()
	alice, _ := svc.CreateRequest(ctx, "alice", match.Preferences{})
	svc.CreateRequest(ctx, "bob", match.Preferences{})

	cand, err := svc.GetPendingCandidate(ctx, "alice", alice.ID)
	if err != nil || cand == nil {
		t.Fatalf("expected a candidate, got %v err=%v", cand, err)
	}
	if len(cand.ScoreReasons) == 0 {
		t.Fatal("expected ScoreReasons to be populated internally (so this test actually proves something)")
	}
	data, err := json.Marshal(cand)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	json.Unmarshal(data, &decoded)
	if _, present := decoded["ScoreReasons"]; present {
		t.Fatal("ScoreReasons must never appear in JSON serialization of a Candidate")
	}
}

func TestListHistory_ReturnsAllOfAccountsRequests(t *testing.T) {
	ctx := context.Background()
	svc := newService()
	r1, _ := svc.CreateRequest(ctx, "alice", match.Preferences{})
	svc.CancelRequest(ctx, "alice", r1.ID)
	svc.CreateRequest(ctx, "alice", match.Preferences{})

	history, err := svc.ListHistory(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 requests in alice's history, got %d", len(history))
	}
}

func TestCancelRequest_OnlyWhileSearching(t *testing.T) {
	ctx := context.Background()
	svc := newService()
	req, _ := svc.CreateRequest(ctx, "alice", match.Preferences{})
	if err := svc.CancelRequest(ctx, "alice", req.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.CancelRequest(ctx, "alice", req.ID); !errors.Is(err, match.ErrRequestNotSearching) {
		t.Fatalf("expected ErrRequestNotSearching on double-cancel, got %v", err)
	}
}
