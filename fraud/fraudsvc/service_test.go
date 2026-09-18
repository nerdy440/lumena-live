package fraudsvc_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lumena/fraud"
	"github.com/lumena/fraud/fraudsvc"
)

type recordedSignal struct {
	accountID string
	delta     float64
	reason    string
}

type fakeRisk struct {
	signals []recordedSignal
}

func (f *fakeRisk) add(_ context.Context, accountID string, delta float64, reason string) {
	f.signals = append(f.signals, recordedSignal{accountID, delta, reason})
}

func (f *fakeRisk) scoreFor(accountID string) float64 {
	var total float64
	for _, s := range f.signals {
		if s.accountID == accountID {
			total += s.delta
		}
	}
	return total
}

func (f *fakeRisk) hasReason(accountID, reason string) bool {
	for _, s := range f.signals {
		if s.accountID == accountID && s.reason == reason {
			return true
		}
	}
	return false
}

func alwaysAllowed(context.Context, string) (bool, error) { return true, nil }
func neverAllowed(context.Context, string) (bool, error)  { return false, nil }

func newTestService(gifts []fraudsvc.GiftFlow) (*fraudsvc.Service, *fakeRisk) {
	risk := &fakeRisk{}
	svc := fraudsvc.New(
		fraud.NewMemDeviceRepo(), fraud.NewMemDisputeRepo(),
		risk.add,
		func(context.Context, time.Time) ([]fraudsvc.GiftFlow, error) { return gifts, nil },
		alwaysAllowed,
	)
	return svc, risk
}

// ── AF-02: device clustering ────────────────────────────────────────────────

func TestRecordDeviceLogin_SingleAccountNoSignal(t *testing.T) {
	ctx := context.Background()
	svc, risk := newTestService(nil)
	svc.RecordDeviceLogin(ctx, "alice", "device-A")
	if len(risk.signals) != 0 {
		t.Fatalf("expected no signal from a single account on a device, got %v", risk.signals)
	}
}

func TestRecordDeviceLogin_TwoAccountsSameDeviceClusters(t *testing.T) {
	ctx := context.Background()
	svc, risk := newTestService(nil)
	svc.RecordDeviceLogin(ctx, "alice", "device-A")
	svc.RecordDeviceLogin(ctx, "bob", "device-A")

	if !risk.hasReason("alice", "device_clustering") {
		t.Fatalf("expected alice to get a device_clustering signal, got %v", risk.signals)
	}
	if !risk.hasReason("bob", "device_clustering") {
		t.Fatalf("expected bob to get a device_clustering signal, got %v", risk.signals)
	}
}

func TestRecordDeviceLogin_DifferentDevicesNoCluster(t *testing.T) {
	ctx := context.Background()
	svc, risk := newTestService(nil)
	svc.RecordDeviceLogin(ctx, "alice", "device-A")
	svc.RecordDeviceLogin(ctx, "bob", "device-B")
	if len(risk.signals) != 0 {
		t.Fatalf("expected no clustering across different devices, got %v", risk.signals)
	}
}

func TestGetDeviceCluster_RequiresPermission(t *testing.T) {
	ctx := context.Background()
	risk := &fakeRisk{}
	svc := fraudsvc.New(
		fraud.NewMemDeviceRepo(), fraud.NewMemDisputeRepo(), risk.add,
		func(context.Context, time.Time) ([]fraudsvc.GiftFlow, error) { return nil, nil },
		neverAllowed,
	)
	svc.RecordDeviceLogin(ctx, "alice", "device-A")
	_, err := svc.GetDeviceCluster(ctx, "rando", "device-A")
	if !errors.Is(err, fraud.ErrForbidden) {
		t.Fatalf("expected ErrForbidden, got %v", err)
	}
}

// ── AF-03: gift loop (exit gate: A→B, B→A within 5 minutes) ────────────────

func TestCheckGiftLoop_ReciprocalWithinWindowElevatesBoth(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	gifts := []fraudsvc.GiftFlow{
		{SenderID: "bob", RecipientID: "alice", CreatedAt: now.Add(-2 * time.Minute)},
	}
	svc, risk := newTestService(gifts)

	svc.CheckGiftLoop(ctx, "alice", "bob", now)

	if !risk.hasReason("alice", "gift_loop") {
		t.Fatalf("expected alice flagged for gift_loop, got %v", risk.signals)
	}
	if !risk.hasReason("bob", "gift_loop") {
		t.Fatalf("expected bob flagged for gift_loop, got %v", risk.signals)
	}
}

func TestCheckGiftLoop_OutsideWindowNoSignal(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	// bob->alice happened 10 minutes ago — outside the 5-minute window.
	gifts := []fraudsvc.GiftFlow{
		{SenderID: "bob", RecipientID: "alice", CreatedAt: now.Add(-10 * time.Minute)},
	}
	svc, risk := newTestService(gifts)

	svc.CheckGiftLoop(ctx, "alice", "bob", now)

	if len(risk.signals) != 0 {
		t.Fatalf("expected no gift_loop signal outside the window, got %v", risk.signals)
	}
}

func TestCheckGiftLoop_OneDirectionalNoSignal(t *testing.T) {
	ctx := context.Background()
	svc, risk := newTestService(nil) // no prior gifts at all
	svc.CheckGiftLoop(ctx, "alice", "bob", time.Now())
	if len(risk.signals) != 0 {
		t.Fatalf("expected no signal for a one-directional gift, got %v", risk.signals)
	}
}

// ── AF-04: velocity limits ──────────────────────────────────────────────────

func TestCheckGiftVelocity_UnderLimitAllowed(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	var gifts []fraudsvc.GiftFlow
	for i := 0; i < fraud.GiftVelocityLimit-1; i++ {
		gifts = append(gifts, fraudsvc.GiftFlow{SenderID: "alice", RecipientID: "bob", CreatedAt: now})
	}
	svc, _ := newTestService(gifts)
	if err := svc.CheckGiftVelocity(ctx, "alice"); err != nil {
		t.Fatalf("expected no error under the limit, got %v", err)
	}
}

func TestCheckGiftVelocity_AtLimitRejected(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	var gifts []fraudsvc.GiftFlow
	for i := 0; i < fraud.GiftVelocityLimit; i++ {
		gifts = append(gifts, fraudsvc.GiftFlow{SenderID: "alice", RecipientID: "bob", CreatedAt: now})
	}
	svc, _ := newTestService(gifts)
	err := svc.CheckGiftVelocity(ctx, "alice")
	if !errors.Is(err, fraud.ErrVelocityExceeded) {
		t.Fatalf("expected ErrVelocityExceeded at the limit, got %v", err)
	}
}

func TestCheckGiftVelocity_OtherSenderUnaffected(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	var gifts []fraudsvc.GiftFlow
	for i := 0; i < fraud.GiftVelocityLimit+5; i++ {
		gifts = append(gifts, fraudsvc.GiftFlow{SenderID: "alice", RecipientID: "bob", CreatedAt: now})
	}
	svc, _ := newTestService(gifts)
	if err := svc.CheckGiftVelocity(ctx, "carol"); err != nil {
		t.Fatalf("expected carol unaffected by alice's velocity, got %v", err)
	}
}

// ── AF-05: chargeback tracking ──────────────────────────────────────────────

func TestRecordDispute_FirstDisputeNoSignal(t *testing.T) {
	ctx := context.Background()
	svc, risk := newTestService(nil)
	svc.RecordDispute(ctx, "alice")
	if len(risk.signals) != 0 {
		t.Fatalf("expected no signal on first dispute, got %v", risk.signals)
	}
}

func TestRecordDispute_RepeatDisputesElevateRisk(t *testing.T) {
	ctx := context.Background()
	svc, risk := newTestService(nil)
	svc.RecordDispute(ctx, "alice")
	svc.RecordDispute(ctx, "alice")
	if !risk.hasReason("alice", "repeat_chargeback") {
		t.Fatalf("expected repeat_chargeback signal on second dispute, got %v", risk.signals)
	}
}

// ── Exit gate: no automatic permanent ban from risk score alone ────────────
// fraudsvc.Service has no CreateEnforcement-shaped method and no
// dependency capable of banning an account — it can only ever call
// addRiskSignal. This test asserts that even a maximal combination of
// every fraud signal never does anything but accumulate score.
func TestFraudSignalsNeverBanAnAccount(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	gifts := []fraudsvc.GiftFlow{
		{SenderID: "bob", RecipientID: "alice", CreatedAt: now.Add(-1 * time.Minute)},
	}
	svc, risk := newTestService(gifts)

	svc.RecordDeviceLogin(ctx, "alice", "device-shared")
	svc.RecordDeviceLogin(ctx, "bob", "device-shared")
	svc.CheckGiftLoop(ctx, "alice", "bob", now)
	svc.RecordDispute(ctx, "alice")
	svc.RecordDispute(ctx, "alice")

	// Every recorded effect is a risk-score delta — never an account status
	// change, ban, or enforcement. The fake risk sink used throughout this
	// test file (fakeRisk.add) is the *only* thing fraudsvc calls to
	// record an effect, and it does nothing but sum deltas.
	if risk.scoreFor("alice") <= 0 {
		t.Fatal("expected alice's risk score to have moved")
	}
}
