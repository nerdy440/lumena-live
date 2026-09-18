package ledger_test

import (
	"testing"
	"time"

	"github.com/lumena/ledger"
)

func TestApplyCapChange_LoweringIsImmediate(t *testing.T) {
	now := time.Now()
	current := &ledger.SpendLimits{AccountID: "alice", DailyCap: 1000}
	next := ledger.ApplyCapChange(current, 500, 0, 0, now)
	if next.DailyCap != 500 {
		t.Fatalf("expected immediate lower to 500, got %d", next.DailyCap)
	}
	if !next.PendingEffectiveAt.IsZero() {
		t.Fatal("expected no pending change after a lower")
	}
}

func TestApplyCapChange_RaisingIsDeferred48Hours(t *testing.T) {
	now := time.Now()
	current := &ledger.SpendLimits{AccountID: "alice", DailyCap: 500}
	next := ledger.ApplyCapChange(current, 2000, 0, 0, now)
	if next.DailyCap != 500 {
		t.Fatalf("expected cap to stay at 500 until the delay elapses, got %d", next.DailyCap)
	}
	if next.PendingDailyCap != 2000 {
		t.Fatalf("expected pending raise to 2000, got %d", next.PendingDailyCap)
	}
	wantEffective := now.Add(48 * time.Hour)
	if next.PendingEffectiveAt.Sub(wantEffective).Abs() > time.Second {
		t.Fatalf("expected 48h delay, got effective at %v (want ~%v)", next.PendingEffectiveAt, wantEffective)
	}

	// Before the delay elapses, resolving does nothing.
	stillOld := ledger.ResolvePending(next, now.Add(47*time.Hour))
	if stillOld.DailyCap != 500 {
		t.Fatalf("expected cap still 500 before delay elapses, got %d", stillOld.DailyCap)
	}

	// After the delay, the raise takes effect.
	resolved := ledger.ResolvePending(next, now.Add(49*time.Hour))
	if resolved.DailyCap != 2000 {
		t.Fatalf("expected cap raised to 2000 after 48h, got %d", resolved.DailyCap)
	}
}

func TestApplyCapChange_LoweringCancelsAPendingRaise(t *testing.T) {
	now := time.Now()
	current := &ledger.SpendLimits{AccountID: "alice", DailyCap: 500}
	withPendingRaise := ledger.ApplyCapChange(current, 2000, 0, 0, now)

	next := ledger.ApplyCapChange(withPendingRaise, 300, 0, 0, now)
	if next.DailyCap != 300 {
		t.Fatalf("expected immediate lower to 300, got %d", next.DailyCap)
	}
	if next.PendingDailyCap != 0 {
		t.Fatalf("expected the pending raise to 2000 to be cancelled, got pending=%d", next.PendingDailyCap)
	}
}
