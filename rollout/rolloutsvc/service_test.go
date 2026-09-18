package rolloutsvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lumena/rollout"
	"github.com/lumena/rollout/rolloutsvc"
)

func alwaysAllowed(context.Context, string) (bool, error) { return true, nil }
func neverAllowed(context.Context, string) (bool, error)  { return false, nil }

func newTestService(allow rolloutsvc.RequirePermissionFunc) *rolloutsvc.Service {
	return rolloutsvc.New(
		rollout.NewMemFlagRepo(), rollout.NewMemInternalRepo(), rollout.NewMemMetricsRepo(),
		allow, func(context.Context, string, string, string) {},
	)
}

func TestSetStage_RequiresPermission(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(neverAllowed)
	_, err := svc.SetStage(ctx, "rando", "new_feed_ranking", rollout.Stage5Pct)
	if !errors.Is(err, rollout.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestSetStage_RejectsUnknownStage(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	_, err := svc.SetStage(ctx, "admin1", "new_feed_ranking", rollout.Stage("nonsense"))
	if !errors.Is(err, rollout.ErrInvalidStage) {
		t.Fatalf("expected ErrInvalidStage, got %v", err)
	}
}

func TestEvaluate_UnregisteredFlagDefaultsOff(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	on, err := svc.Evaluate(ctx, "never_created", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Fatal("expected an unregistered flag to evaluate false, not error or true")
	}
}

func TestEvaluate_OffIsAlwaysFalse(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	if _, err := svc.SetStage(ctx, "admin1", "f1", rollout.StageOff); err != nil {
		t.Fatal(err)
	}
	for _, acct := range []string{"alice", "bob", "carol", "dave"} {
		on, err := svc.Evaluate(ctx, "f1", acct)
		if err != nil {
			t.Fatal(err)
		}
		if on {
			t.Fatalf("expected off stage to always evaluate false, got true for %s", acct)
		}
	}
}

func TestEvaluate_FullIsAlwaysTrue(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	if _, err := svc.SetStage(ctx, "admin1", "f1", rollout.StageFull); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		on, err := svc.Evaluate(ctx, "f1", accountName(i))
		if err != nil {
			t.Fatal(err)
		}
		if !on {
			t.Fatalf("expected 100pct stage to always evaluate true, got false for account %d", i)
		}
	}
}

func TestEvaluate_InternalStageOnlyInternalAccounts(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	if _, err := svc.SetStage(ctx, "admin1", "f1", rollout.StageInternal); err != nil {
		t.Fatal(err)
	}
	on, err := svc.Evaluate(ctx, "f1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if on {
		t.Fatal("expected non-internal account to evaluate false at the internal stage")
	}
	if err := svc.DevMarkInternal(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	on, err = svc.Evaluate(ctx, "f1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("expected internal account to evaluate true at the internal stage")
	}
}

// TestEvaluate_PercentageStageIsApproximatelyRightAndDeterministic is the
// core staged-rollout correctness check: at 25pct, roughly a quarter of a
// large account population should land in the cohort, and re-evaluating
// the same account against the same flag must always return the same
// answer (a real rollout can't flip-flop a user in and out every request).
func TestEvaluate_PercentageStageIsApproximatelyRightAndDeterministic(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	if _, err := svc.SetStage(ctx, "admin1", "f1", rollout.Stage25Pct); err != nil {
		t.Fatal(err)
	}

	const n = 2000
	enabled := 0
	for i := 0; i < n; i++ {
		acct := accountName(i)
		on, err := svc.Evaluate(ctx, "f1", acct)
		if err != nil {
			t.Fatal(err)
		}
		// Determinism: evaluating again must agree.
		on2, _ := svc.Evaluate(ctx, "f1", acct)
		if on != on2 {
			t.Fatalf("account %s flip-flopped between two evaluations of the same flag", acct)
		}
		if on {
			enabled++
		}
	}
	pct := float64(enabled) / float64(n) * 100
	if pct < 20 || pct > 30 {
		t.Fatalf("expected roughly 25%% of %d accounts enabled at Stage25Pct, got %.1f%% (%d)", n, pct, enabled)
	}
}

// TestEvaluate_DifferentFlagsBucketIndependently proves one account isn't
// permanently "lucky" or "unlucky" across every flag — the hash includes
// the flag key, not just the account. Uses Stage25Pct (not a low
// percentage) specifically so chance agreement between two independent
// coin flips isn't the dominant signal: at 5% each, two *independent*
// flags would already agree ~90% of the time simply because "both off" is
// overwhelmingly likely — a naive ">50% agreement = not independent"
// check at low percentages is a statistics bug, not a real assertion.
func TestEvaluate_DifferentFlagsBucketIndependently(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	if _, err := svc.SetStage(ctx, "admin1", "flag_a", rollout.Stage25Pct); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetStage(ctx, "admin1", "flag_b", rollout.Stage25Pct); err != nil {
		t.Fatal(err)
	}
	const n = 2000
	var enabledA, enabledB []string
	for i := 0; i < n; i++ {
		acct := accountName(i)
		a, _ := svc.Evaluate(ctx, "flag_a", acct)
		b, _ := svc.Evaluate(ctx, "flag_b", acct)
		if a {
			enabledA = append(enabledA, acct)
		}
		if b {
			enabledB = append(enabledB, acct)
		}
	}
	if len(enabledA) == 0 || len(enabledB) == 0 {
		t.Fatal("test setup invalid: expected both flags to have some accounts enabled at 25pct")
	}
	setB := make(map[string]bool, len(enabledB))
	for _, a := range enabledB {
		setB[a] = true
	}
	overlap := 0
	for _, a := range enabledA {
		if setB[a] {
			overlap++
		}
	}
	// Under independence, expected overlap fraction of setA that's also in
	// setB is ~25% (setB's own enable rate). If bucketing ignored the flag
	// key, setA and setB would be IDENTICAL (100% overlap). Anything well
	// below "identical" is enough to demonstrate the flag key matters.
	overlapPct := float64(overlap) / float64(len(enabledA)) * 100
	if overlapPct > 60 {
		t.Fatalf("flag_a's enabled set overlaps %.1f%% with flag_b's — expected ~25%% under independent bucketing, got something close to identical (bucketing may be ignoring the flag key)", overlapPct)
	}
}

// TestRollback_ImmediatelyRemovesEntireCohort is doc 12 Phase 20's exit
// gate: "Rollback runbook tested." The runbook's actual mechanism is
// "set the flag back to off" — this proves that action really does empty
// the cohort immediately for every account that was previously in it, not
// just for new evaluations of some but not others.
func TestRollback_ImmediatelyRemovesEntireCohort(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	if _, err := svc.SetStage(ctx, "admin1", "risky_feature", rollout.Stage25Pct); err != nil {
		t.Fatal(err)
	}

	const n = 500
	wasEnabled := make(map[string]bool, n)
	anyEnabled := false
	for i := 0; i < n; i++ {
		acct := accountName(i)
		on, _ := svc.Evaluate(ctx, "risky_feature", acct)
		wasEnabled[acct] = on
		anyEnabled = anyEnabled || on
	}
	if !anyEnabled {
		t.Fatal("test setup invalid: expected at least one account enabled at 25pct before rollback")
	}

	// The rollback action.
	if _, err := svc.SetStage(ctx, "admin1", "risky_feature", rollout.StageOff); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < n; i++ {
		acct := accountName(i)
		on, err := svc.Evaluate(ctx, "risky_feature", acct)
		if err != nil {
			t.Fatal(err)
		}
		if on {
			t.Fatalf("account %s (enabled=%v before rollback) still evaluates true after rolling the flag back to off", acct, wasEnabled[acct])
		}
	}
}

func TestGetCanaryMetrics_RequiresPermission(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(neverAllowed)
	_, err := svc.GetCanaryMetrics(ctx, "rando")
	if !errors.Is(err, rollout.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

func TestCanaryMetrics_ComputesErrorRateLatencyGiftRateAndCrashRate(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)

	svc.RecordRequest(ctx, rollout.CohortControl, 200, 10*time.Millisecond)
	svc.RecordRequest(ctx, rollout.CohortControl, 200, 20*time.Millisecond)
	svc.RecordRequest(ctx, rollout.CohortControl, 500, 30*time.Millisecond)
	svc.RecordGiftAttempt(ctx, rollout.CohortControl, true)
	svc.RecordGiftAttempt(ctx, rollout.CohortControl, true)
	svc.RecordGiftAttempt(ctx, rollout.CohortControl, false)
	svc.RecordPanic(ctx, rollout.CohortControl)

	m, err := svc.GetCanaryMetrics(ctx, "admin1")
	if err != nil {
		t.Fatal(err)
	}
	c := m.Control
	if c.RequestCount != 3 {
		t.Fatalf("expected 3 requests, got %d", c.RequestCount)
	}
	if c.ErrorCount != 1 {
		t.Fatalf("expected 1 error (the 500), got %d", c.ErrorCount)
	}
	wantErrRate := 100.0 / 3.0
	if c.ErrorRatePct < wantErrRate-0.01 || c.ErrorRatePct > wantErrRate+0.01 {
		t.Fatalf("expected error rate ~%.2f%%, got %.2f%%", wantErrRate, c.ErrorRatePct)
	}
	if c.GiftAttempts != 3 || c.GiftSuccesses != 2 {
		t.Fatalf("expected 3 gift attempts / 2 successes, got %d/%d", c.GiftAttempts, c.GiftSuccesses)
	}
	wantGiftRate := 200.0 / 3.0
	if c.GiftSuccessRatePct < wantGiftRate-0.01 || c.GiftSuccessRatePct > wantGiftRate+0.01 {
		t.Fatalf("expected gift success rate ~%.2f%%, got %.2f%%", wantGiftRate, c.GiftSuccessRatePct)
	}
	if c.PanicCount != 1 {
		t.Fatalf("expected 1 panic recorded, got %d", c.PanicCount)
	}
	// Canary cohort untouched — must report zeroes, not error.
	if m.Canary.RequestCount != 0 {
		t.Fatalf("expected canary cohort untouched, got %+v", m.Canary)
	}
}

// TestCohortFor_AnonymousIsControlAtPercentageStages: a percentage stage
// has no account to hash for an anonymous request, so it can't be
// deterministically bucketed — it must default to control rather than
// (say) crashing or randomly flip-flopping per request. (At StageFull,
// "canary" for anonymous traffic too is the correct, intentional
// behavior — 100% means everyone, not "everyone with an account.")
func TestCohortFor_AnonymousIsControlAtPercentageStages(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(alwaysAllowed)
	if _, err := svc.SetStage(ctx, "admin1", rollout.CanaryFlagKey, rollout.Stage25Pct); err != nil {
		t.Fatal(err)
	}
	if got := svc.CohortFor(ctx, ""); got != rollout.CohortControl {
		t.Fatalf("expected anonymous requests to be control at a percentage stage (no account to bucket), got %s", got)
	}
}

func accountName(i int) string {
	return "acc-" + string(rune('a'+i%26)) + itoaTest(i)
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
