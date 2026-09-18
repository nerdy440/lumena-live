// Package fraudsvc implements the fraud prevention service (roadmap Phase
// 18). Every dependency on ledger/moderation/admin is injected as a
// closure, matching the DI convention used throughout this codebase.
package fraudsvc

import (
	"context"
	"time"

	"github.com/lumena/fraud"
)

// ─── Injected closures ──────────────────────────────────────────────────────

// AddRiskSignalFunc adapts modsvc.RiskEngine.AddSignal — every fraud
// signal lands in the SAME risk scoring pipeline moderation's grooming
// engine already writes to, never a second parallel one.
type AddRiskSignalFunc func(ctx context.Context, accountID string, delta float64, reason string)

type GiftFlow struct {
	SenderID    string
	RecipientID string
	CreatedAt   time.Time
}

// ListRecentGiftsFunc adapts ledger.Repo (via MemLedger.ListByKind),
// read-only — reused for both gift-loop detection and velocity checks
// instead of a separate copy of gift history.
type ListRecentGiftsFunc func(ctx context.Context, since time.Time) ([]GiftFlow, error)

// RequirePermissionFunc adapts adminsvc.Service.HasPermission(ctx,
// actorID, admin.PermFraudReview) for the device-cluster lookup endpoint.
type RequirePermissionFunc func(ctx context.Context, actorID string) (bool, error)

// ─── Repos ──────────────────────────────────────────────────────────────────

type DeviceRepo interface {
	Record(ctx context.Context, accountID, deviceHash string) ([]string, error)
	AccountsForDevice(ctx context.Context, deviceHash string) ([]string, error)
}

type DisputeRepo interface {
	Increment(ctx context.Context, accountID string) (int, error)
}

// ─── Service ────────────────────────────────────────────────────────────────

type Service struct {
	devices  DeviceRepo
	disputes DisputeRepo

	addRiskSignal     AddRiskSignalFunc
	listRecentGifts   ListRecentGiftsFunc
	requirePermission RequirePermissionFunc

	now func() time.Time
}

func New(devices DeviceRepo, disputes DisputeRepo, addRiskSignal AddRiskSignalFunc, listRecentGifts ListRecentGiftsFunc, requirePermission RequirePermissionFunc) *Service {
	return &Service{
		devices: devices, disputes: disputes,
		addRiskSignal: addRiskSignal, listRecentGifts: listRecentGifts, requirePermission: requirePermission,
		now: time.Now,
	}
}

// ─── AF-02: device/IP fingerprint clustering ───────────────────────────────

// RecordDeviceLogin is authsvc's DeviceLoginHook target. When deviceHash
// is now shared by >= DeviceClusterMinAccounts distinct accounts, every
// account in the cluster gets a risk signal — doc 12 Phase 18's exit gate:
// "same device, multiple accounts clusters correctly."
func (s *Service) RecordDeviceLogin(ctx context.Context, accountID, deviceHash string) {
	accounts, err := s.devices.Record(ctx, accountID, deviceHash)
	if err != nil || len(accounts) < fraud.DeviceClusterMinAccounts {
		return
	}
	for _, id := range accounts {
		s.addRiskSignal(ctx, id, fraud.SignalDeviceClustering, "device_clustering")
	}
}

// GetDeviceCluster is the admin-facing lookup (fraud.review permission).
func (s *Service) GetDeviceCluster(ctx context.Context, actorID, deviceHash string) ([]string, error) {
	ok, err := s.requirePermission(ctx, actorID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fraud.ErrForbidden
	}
	return s.devices.AccountsForDevice(ctx, deviceHash)
}

// ─── AF-03: gift-loop detection ────────────────────────────────────────────

// CheckGiftLoop is giftsvc's SentHook target. If recipientID already sent
// senderID a gift within the last fraud.GiftLoopWindow, this is an A→B/B→A
// circular flow — doc 12 Phase 18's exit gate: "A→B, B→A within 5 minutes
// elevates risk score." Elevates both accounts, since either side of a
// wash-trading pair benefits.
func (s *Service) CheckGiftLoop(ctx context.Context, senderID, recipientID string, at time.Time) {
	windowStart := at.Add(-fraud.GiftLoopWindow)
	gifts, err := s.listRecentGifts(ctx, windowStart)
	if err != nil {
		return
	}
	// listRecentGifts's "since" contract is advisory (the real ledger-backed
	// closure honors it, but callers — tests included — aren't required
	// to), so re-check the window explicitly rather than trusting it blindly.
	for _, g := range gifts {
		if g.SenderID == recipientID && g.RecipientID == senderID && !g.CreatedAt.Before(windowStart) {
			s.addRiskSignal(ctx, senderID, fraud.SignalGiftLoop, "gift_loop")
			s.addRiskSignal(ctx, recipientID, fraud.SignalGiftLoop, "gift_loop")
			return
		}
	}
}

// ─── AF-04: velocity limits ─────────────────────────────────────────────

// CheckGiftVelocity is giftsvc's PreSendFunc target. Returns
// fraud.ErrVelocityExceeded if senderID has already sent
// fraud.GiftVelocityLimit gifts within fraud.GiftVelocityWindow.
func (s *Service) CheckGiftVelocity(ctx context.Context, senderID string) error {
	windowStart := s.now().Add(-fraud.GiftVelocityWindow)
	gifts, err := s.listRecentGifts(ctx, windowStart)
	if err != nil {
		return nil // fail open — a read error here must never block a legitimate gift
	}
	count := 0
	for _, g := range gifts {
		if g.SenderID == senderID && !g.CreatedAt.Before(windowStart) {
			count++
		}
	}
	if count >= fraud.GiftVelocityLimit {
		return fraud.ErrVelocityExceeded
	}
	return nil
}

// ─── AF-05: chargeback tracking ─────────────────────────────────────────

// RecordDispute is ordersvc's DisputeHook target. Repeat disputes (doc 02
// AF-05: "chargeback & refund-abuse tracking") elevate risk.
func (s *Service) RecordDispute(ctx context.Context, accountID string) {
	count, err := s.disputes.Increment(ctx, accountID)
	if err != nil {
		return
	}
	if count >= fraud.ChargebackRiskThreshold {
		s.addRiskSignal(ctx, accountID, fraud.SignalRepeatChargeback, "repeat_chargeback")
	}
}
