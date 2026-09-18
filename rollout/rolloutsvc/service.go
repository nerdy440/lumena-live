// Package rolloutsvc implements feature flags, staged rollout, and canary
// metrics (roadmap Phase 20). Every dependency on admin is injected as a
// closure, matching the DI convention used throughout this codebase.
package rolloutsvc

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/lumena/rollout"
)

// RequirePermissionFunc adapts adminsvc.Service.HasPermission(ctx,
// actorID, admin.PermRolloutManage).
type RequirePermissionFunc func(ctx context.Context, actorID string) (bool, error)

// AuditFunc adapts adminsvc-style audit logging for stage changes —
// deployment control is the most sensitive action in this codebase, so
// every stage change is recorded the same way admin's account
// restrict/unsuspend actions are.
type AuditFunc func(ctx context.Context, actorID, action, detail string)

// ─── Repos ──────────────────────────────────────────────────────────────

type FlagRepo interface {
	Get(ctx context.Context, key string) (*rollout.FeatureFlag, error)
	Upsert(ctx context.Context, key string, stage rollout.Stage) (*rollout.FeatureFlag, error)
	List(ctx context.Context) ([]rollout.FeatureFlag, error)
}

type InternalRepo interface {
	IsInternal(ctx context.Context, accountID string) (bool, error)
	MarkInternal(ctx context.Context, accountID string) error
}

type MetricsRepo interface {
	RecordRequest(ctx context.Context, cohort rollout.Cohort, status int, latency time.Duration) error
	RecordGiftAttempt(ctx context.Context, cohort rollout.Cohort, success bool) error
	RecordPanic(ctx context.Context, cohort rollout.Cohort) error
	Snapshot(ctx context.Context, cohort rollout.Cohort) (rollout.Metrics, error)
}

// ─── Service ────────────────────────────────────────────────────────────

type Service struct {
	flags    FlagRepo
	internal InternalRepo
	metrics  MetricsRepo

	requirePermission RequirePermissionFunc
	audit             AuditFunc
}

func New(flags FlagRepo, internal InternalRepo, metrics MetricsRepo, requirePermission RequirePermissionFunc, audit AuditFunc) *Service {
	return &Service{flags: flags, internal: internal, metrics: metrics, requirePermission: requirePermission, audit: audit}
}

func (s *Service) requirePerm(ctx context.Context, actorID string) error {
	ok, err := s.requirePermission(ctx, actorID)
	if err != nil {
		return err
	}
	if !ok {
		return rollout.ErrForbidden
	}
	return nil
}

// ─── Flags / staged rollout ─────────────────────────────────────────────

// Evaluate reports whether flagKey is enabled for accountID — the actual
// staged-rollout mechanism. Off is always false, 100pct is always true;
// everything in between uses a deterministic hash of (flagKey, accountID)
// so the same account always lands on the same side of the rollout for a
// given flag, and different flags bucket independently (doc 12 Phase 20:
// "internal → 1% → 5% → 25% → 100%").
func (s *Service) Evaluate(ctx context.Context, flagKey, accountID string) (bool, error) {
	f, err := s.flags.Get(ctx, flagKey)
	if err != nil {
		if err == rollout.ErrFlagNotFound {
			return false, nil // an unregistered flag defaults off, never errors a caller
		}
		return false, err
	}
	switch f.Stage {
	case rollout.StageOff:
		return false, nil
	case rollout.StageFull:
		return true, nil
	case rollout.StageInternal:
		if accountID == "" {
			return false, nil
		}
		return s.internal.IsInternal(ctx, accountID)
	default:
		if accountID == "" {
			return false, nil
		}
		return bucket(flagKey, accountID) < rollout.Percent(f.Stage), nil
	}
}

// bucket deterministically maps (flagKey, accountID) to [0, 100).
func bucket(flagKey, accountID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(flagKey + ":" + accountID))
	return int(h.Sum32() % 100)
}

func (s *Service) GetFlag(ctx context.Context, key string) (*rollout.FeatureFlag, error) {
	return s.flags.Get(ctx, key)
}

func (s *Service) ListFlags(ctx context.Context) ([]rollout.FeatureFlag, error) {
	return s.flags.List(ctx)
}

// SetStage is the staged-rollout control (and its own rollback path — set
// back to StageOff). RBAC-gated: rollout.manage is superadmin-only, since
// this is the single most blast-radius-heavy action in the admin surface.
func (s *Service) SetStage(ctx context.Context, actorID, flagKey string, stage rollout.Stage) (*rollout.FeatureFlag, error) {
	if err := s.requirePerm(ctx, actorID); err != nil {
		return nil, err
	}
	if !rollout.ValidStage(stage) {
		return nil, rollout.ErrInvalidStage
	}
	f, err := s.flags.Upsert(ctx, flagKey, stage)
	if err != nil {
		return nil, err
	}
	if s.audit != nil {
		s.audit(ctx, actorID, "rollout_stage_change", flagKey+" -> "+string(stage))
	}
	return f, nil
}

// DevMarkInternal grants "internal" rollout eligibility — standing in for
// the real staff directory this dev build doesn't have. NEVER ships to
// production, same pattern as every other DevGrant in this codebase.
func (s *Service) DevMarkInternal(ctx context.Context, accountID string) error {
	return s.internal.MarkInternal(ctx, accountID)
}

// ─── Canary metrics ─────────────────────────────────────────────────────

func (s *Service) RecordRequest(ctx context.Context, cohort rollout.Cohort, status int, latency time.Duration) {
	_ = s.metrics.RecordRequest(ctx, cohort, status, latency)
}

func (s *Service) RecordGiftAttempt(ctx context.Context, cohort rollout.Cohort, success bool) {
	_ = s.metrics.RecordGiftAttempt(ctx, cohort, success)
}

func (s *Service) RecordPanic(ctx context.Context, cohort rollout.Cohort) {
	_ = s.metrics.RecordPanic(ctx, cohort)
}

func (s *Service) GetCanaryMetrics(ctx context.Context, actorID string) (*rollout.MetricsComparison, error) {
	if err := s.requirePerm(ctx, actorID); err != nil {
		return nil, err
	}
	control, err := s.metrics.Snapshot(ctx, rollout.CohortControl)
	if err != nil {
		return nil, err
	}
	canary, err := s.metrics.Snapshot(ctx, rollout.CohortCanary)
	if err != nil {
		return nil, err
	}
	return &rollout.MetricsComparison{Control: control, Canary: canary}, nil
}

// CohortFor evaluates the designated canary flag for accountID (or ""
// for an anonymous/unauthenticated request) — the metrics middleware
// calls this on every request to decide which side of the comparison it
// belongs to.
func (s *Service) CohortFor(ctx context.Context, accountID string) rollout.Cohort {
	isCanary, err := s.Evaluate(ctx, rollout.CanaryFlagKey, accountID)
	if err != nil || !isCanary {
		return rollout.CohortControl
	}
	return rollout.CohortCanary
}
