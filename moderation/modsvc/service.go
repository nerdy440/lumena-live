// Package modsvc implements the moderation service (roadmap Phase 15):
// reports, moderator triage, rule-bound enforcements, appeals, and a
// grooming-pattern risk engine. Every dependency on auth/profile/chat/pv is
// injected as a closure, matching the DI convention used by creatorsvc,
// matchsvc and chatsvc.
package modsvc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lumena/moderation"
)

// ─── Injected closures ──────────────────────────────────────────────────────

// ApplyEnforcementFunc applies action's real account-level effect —
// adapts authsvc.Service.SetAccountStatus. Mute has no account-wide status
// (it's room/chat scoped, handled by the existing WS moderation controls
// from Phase 6) so the adapter is a no-op for ActionWarn/ActionMute.
type ApplyEnforcementFunc func(ctx context.Context, accountID string, action moderation.EnforcementAction) error

// ReinstateFunc reverts an account to active standing — used when an appeal
// is overturned. Adapts authsvc.Service.SetAccountStatus(..., StatusActive).
type ReinstateFunc func(ctx context.Context, accountID string) error

// ─── Repos ──────────────────────────────────────────────────────────────────

type ReportRepo interface {
	RecentlyReported(ctx context.Context, reporterID, subjectID string, within time.Duration) (bool, error)
	Create(ctx context.Context, r moderation.Report) (*moderation.Report, error)
	Get(ctx context.Context, id string) (*moderation.Report, error)
	UpdateState(ctx context.Context, id string, state moderation.ReportState) error
	List(ctx context.Context, stateFilter moderation.ReportState) ([]moderation.Report, error)
}

type EnforcementRepo interface {
	Create(ctx context.Context, e moderation.Enforcement) (*moderation.Enforcement, error)
	Get(ctx context.Context, id string) (*moderation.Enforcement, error)
	ListByAccount(ctx context.Context, accountID string) ([]moderation.Enforcement, error)
}

type AppealRepo interface {
	HasPending(ctx context.Context, enforcementID string) (bool, error)
	Create(ctx context.Context, a moderation.Appeal) (*moderation.Appeal, error)
	Get(ctx context.Context, id string) (*moderation.Appeal, error)
	Decide(ctx context.Context, id string, state moderation.AppealState, reviewedBy string) (*moderation.Appeal, error)
	List(ctx context.Context, stateFilter moderation.AppealState) ([]moderation.Appeal, error)
}

type RiskRepo interface {
	Get(ctx context.Context, accountID string) (*moderation.RiskProfile, error)
	Upsert(ctx context.Context, p moderation.RiskProfile) error
	ListFlagged(ctx context.Context) ([]moderation.RiskProfile, error)
}

type ModeratorRepo interface {
	IsModerator(ctx context.Context, accountID string) (bool, error)
	Grant(ctx context.Context, accountID string) error
}

// ─── Service ────────────────────────────────────────────────────────────────

type Service struct {
	reports      ReportRepo
	enforcements EnforcementRepo
	appeals      AppealRepo
	moderators   ModeratorRepo
	risk         *RiskEngine

	applyEnforcement ApplyEnforcementFunc
	reinstate        ReinstateFunc

	now func() time.Time
}

func New(
	reports ReportRepo,
	enforcements EnforcementRepo,
	appeals AppealRepo,
	moderators ModeratorRepo,
	risk *RiskEngine,
	applyEnforcement ApplyEnforcementFunc,
	reinstate ReinstateFunc,
) *Service {
	return &Service{
		reports:          reports,
		enforcements:     enforcements,
		appeals:          appeals,
		moderators:       moderators,
		risk:             risk,
		applyEnforcement: applyEnforcement,
		reinstate:        reinstate,
		now:              time.Now,
	}
}

func (s *Service) requireModerator(ctx context.Context, accountID string) error {
	ok, err := s.moderators.IsModerator(ctx, accountID)
	if err != nil {
		return err
	}
	if !ok {
		return moderation.ErrNotModerator
	}
	return nil
}

// ─── Reports (MODR_001) ─────────────────────────────────────────────────────

func (s *Service) SubmitReport(ctx context.Context, reporterID, subjectType, subjectID, reasonCode, detail, evidenceRef string) (*moderation.Report, error) {
	recently, err := s.reports.RecentlyReported(ctx, reporterID, subjectID, moderation.ReportRateLimitWindow)
	if err != nil {
		return nil, err
	}
	if recently {
		return nil, moderation.ErrRateLimited
	}
	return s.reports.Create(ctx, moderation.Report{
		ReporterID: reporterID, SubjectType: subjectType, SubjectID: subjectID,
		ReasonCode: reasonCode, Detail: detail, EvidenceRef: evidenceRef,
		State: moderation.ReportOpen, CreatedAt: s.now(),
	})
}

// ListQueue is the moderator triage queue (moderator-only).
func (s *Service) ListQueue(ctx context.Context, moderatorID string, stateFilter moderation.ReportState) ([]moderation.Report, error) {
	if err := s.requireModerator(ctx, moderatorID); err != nil {
		return nil, err
	}
	return s.reports.List(ctx, stateFilter)
}

func (s *Service) TriageReport(ctx context.Context, moderatorID, reportID string, newState moderation.ReportState) (*moderation.Report, error) {
	if err := s.requireModerator(ctx, moderatorID); err != nil {
		return nil, err
	}
	if err := s.reports.UpdateState(ctx, reportID, newState); err != nil {
		return nil, err
	}
	return s.reports.Get(ctx, reportID)
}

// ─── Rule catalogue ─────────────────────────────────────────────────────────

func (s *Service) ListRules(_ context.Context) []moderation.ModerationRule {
	return moderation.Catalogue
}

// ─── Enforcements ─────────────────────────────────────────────────────────

// CreateEnforcement is the single choke point every enforcement — auto or
// human — must pass through, so the "no automated permanent termination"
// constraint (doc 06 §11's permanent_requires_human, doc 10 §8) is checked
// exactly once rather than trusted to every caller.
func (s *Service) CreateEnforcement(ctx context.Context, decidedBy, accountID, ruleID string, action moderation.EnforcementAction, durationHours int, evidenceRef string) (*moderation.Enforcement, error) {
	if _, ok := moderation.RuleByID(ruleID); !ok {
		return nil, moderation.ErrRuleNotFound
	}
	if !moderation.ValidAction(action) {
		return nil, moderation.ErrInvalidAction
	}
	if action == moderation.ActionTerminate && !strings.HasPrefix(decidedBy, "human:") {
		return nil, moderation.ErrAutomatedTerminationForbidden
	}
	if evidenceRef == "" {
		evidenceRef = "n/a" // schema requires NOT NULL; never actually empty in practice (auto path always sets a session/message ref)
	}

	var expiresAt *time.Time
	if durationHours > 0 {
		t := s.now().Add(time.Duration(durationHours) * time.Hour)
		expiresAt = &t
	}

	e, err := s.enforcements.Create(ctx, moderation.Enforcement{
		AccountID: accountID, RuleID: ruleID, Action: action, DurationHours: durationHours,
		EvidenceRef: evidenceRef, DecidedBy: decidedBy, Appealable: action != moderation.ActionWarn,
		CreatedAt: s.now(), ExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, err
	}

	if s.applyEnforcement != nil {
		if err := s.applyEnforcement(ctx, accountID, action); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (s *Service) ListMyEnforcements(ctx context.Context, accountID string) ([]moderation.Enforcement, error) {
	return s.enforcements.ListByAccount(ctx, accountID)
}

func (s *Service) GetEnforcement(ctx context.Context, accountID, id string) (*moderation.Enforcement, error) {
	e, err := s.enforcements.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if e.AccountID != accountID {
		ok, mErr := s.moderators.IsModerator(ctx, accountID)
		if mErr != nil {
			return nil, mErr
		}
		if !ok {
			return nil, moderation.ErrNotOwner
		}
	}
	return e, nil
}

// ─── Appeals (MODR_003) ──────────────────────────────────────────────────────

func (s *Service) FileAppeal(ctx context.Context, accountID, enforcementID, statement string) (*moderation.Appeal, error) {
	e, err := s.enforcements.Get(ctx, enforcementID)
	if err != nil {
		return nil, err
	}
	if e.AccountID != accountID {
		return nil, moderation.ErrNotOwner
	}
	if !e.Appealable {
		return nil, moderation.ErrNotAppealable
	}
	pending, err := s.appeals.HasPending(ctx, enforcementID)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, moderation.ErrAlreadyAppealed
	}
	return s.appeals.Create(ctx, moderation.Appeal{
		EnforcementID: enforcementID, AccountID: accountID, Statement: statement,
		State: moderation.AppealPending, CreatedAt: s.now(),
	})
}

// DecideAppeal is moderator-only. Overturning reinstates the account —
// doc 10 §8 gives BT-09's transparency+appeal surface teeth: an appeal
// that succeeds must actually undo the restriction, not just change a
// status label nobody acts on.
func (s *Service) DecideAppeal(ctx context.Context, moderatorID, appealID string, uphold bool) (*moderation.Appeal, error) {
	if err := s.requireModerator(ctx, moderatorID); err != nil {
		return nil, err
	}
	state := moderation.AppealUpheld
	if !uphold {
		state = moderation.AppealOverturned
	}
	a, err := s.appeals.Decide(ctx, appealID, state, moderatorID)
	if err != nil {
		return nil, err
	}
	if !uphold && s.reinstate != nil {
		if err := s.reinstate(ctx, a.AccountID); err != nil {
			return nil, err
		}
	}
	return a, nil
}

func (s *Service) ListAppealQueue(ctx context.Context, moderatorID string, stateFilter moderation.AppealState) ([]moderation.Appeal, error) {
	if err := s.requireModerator(ctx, moderatorID); err != nil {
		return nil, err
	}
	return s.appeals.List(ctx, stateFilter)
}

// ─── Moderator role (dev-only escalation) ───────────────────────────────────

// DevGrantModerator grants moderator access, standing in for the RBAC/admin
// system (roadmap Phase 16) that doesn't exist yet — same dev-escalation
// pattern as DevAssureAge and creator's DevApproveKYC. NEVER ships to
// production.
func (s *Service) DevGrantModerator(ctx context.Context, accountID string) error {
	return s.moderators.Grant(ctx, accountID)
}

func (s *Service) IsModerator(ctx context.Context, accountID string) (bool, error) {
	return s.moderators.IsModerator(ctx, accountID)
}

// ─── Risk queue (moderator-only view over the risk engine) ─────────────────

func (s *Service) ListRiskQueue(ctx context.Context, moderatorID string) ([]moderation.RiskProfile, error) {
	if err := s.requireModerator(ctx, moderatorID); err != nil {
		return nil, err
	}
	return s.risk.repo.ListFlagged(ctx)
}

func (s *Service) GetRiskProfile(ctx context.Context, moderatorID, accountID string) (*moderation.RiskProfile, error) {
	if err := s.requireModerator(ctx, moderatorID); err != nil {
		return nil, err
	}
	return s.risk.repo.Get(ctx, accountID)
}

// RecordMessage / RecordPVRequest are exposed so main.go's chatsvc/pvsvc
// hooks can feed the risk engine without the moderation module importing
// those packages.
func (s *Service) RecordMessage(ctx context.Context, senderID, recipientID, body string) {
	s.risk.RecordMessage(ctx, senderID, recipientID, body)
}

func (s *Service) RecordPVRequest(ctx context.Context, callerID, calleeID string) {
	s.risk.RecordPVRequest(ctx, callerID, calleeID)
}

// RecordAutoTermination lets the streaming module's classifier hook (Phase
// 5) create a real, queryable enforcement instead of the termination
// existing only as a stream-session state transition. Restrict, not
// suspend — doc 10 §2c's CSAM pipeline says "account restricted pending
// review," and human review (via appeal or moderator triage) decides
// anything harsher from here.
func (s *Service) RecordAutoTermination(ctx context.Context, hostID, sessionID, ruleID string) {
	_, _ = s.CreateEnforcement(ctx, "auto:classifier", hostID, ruleID, moderation.ActionRestrictBroadcast, 0,
		fmt.Sprintf("session:%s", sessionID))
}
