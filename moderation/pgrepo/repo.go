// Package pgrepo implements modsvc's ReportRepo, EnforcementRepo,
// AppealRepo, RiskRepo, and ModeratorRepo against real Postgres — the
// production counterpart to moderation.Mem*Repo.
package pgrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/moderation"
)

// ─── Reports ────────────────────────────────────────────────────────────────

type ReportRepo struct{ pool *pgxpool.Pool }

func NewReportRepo(pool *pgxpool.Pool) *ReportRepo { return &ReportRepo{pool: pool} }

const reportCols = "id, reporter_id, subject_type, subject_id, reason_code, detail, evidence_ref, state, case_id, created_at"

func scanReport(row pgx.Row) (*moderation.Report, error) {
	var r moderation.Report
	var state string
	err := row.Scan(&r.ID, &r.ReporterID, &r.SubjectType, &r.SubjectID, &r.ReasonCode, &r.Detail, &r.EvidenceRef, &state, &r.CaseID, &r.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, moderation.ErrReportNotFound
	}
	if err != nil {
		return nil, err
	}
	r.State = moderation.ReportState(state)
	return &r, nil
}

func (r *ReportRepo) RecentlyReported(ctx context.Context, reporterID, subjectID string, within time.Duration) (bool, error) {
	var recent bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM moderation_reports
			WHERE reporter_id = $1 AND subject_id = $2 AND created_at > $3
		)`, reporterID, subjectID, time.Now().Add(-within)).Scan(&recent)
	return recent, err
}

func (r *ReportRepo) Create(ctx context.Context, rep moderation.Report) (*moderation.Report, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('moderation_report_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	rep.ID = fmt.Sprintf("report-%04d", seq)
	rep.CaseID = fmt.Sprintf("RPT-%04d", seq)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO moderation_reports (id, reporter_id, subject_type, subject_id, reason_code, detail, evidence_ref, state, case_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
		RETURNING `+reportCols,
		rep.ID, rep.ReporterID, rep.SubjectType, rep.SubjectID, rep.ReasonCode, rep.Detail, rep.EvidenceRef, string(rep.State), rep.CaseID)
	return scanReport(row)
}

func (r *ReportRepo) Get(ctx context.Context, id string) (*moderation.Report, error) {
	return scanReport(r.pool.QueryRow(ctx, `SELECT `+reportCols+` FROM moderation_reports WHERE id = $1`, id))
}

func (r *ReportRepo) UpdateState(ctx context.Context, id string, state moderation.ReportState) error {
	tag, err := r.pool.Exec(ctx, `UPDATE moderation_reports SET state = $2 WHERE id = $1`, id, string(state))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return moderation.ErrReportNotFound
	}
	return nil
}

func (r *ReportRepo) List(ctx context.Context, stateFilter moderation.ReportState) ([]moderation.Report, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+reportCols+` FROM moderation_reports
		WHERE $1 = '' OR state = $1
		ORDER BY created_at DESC`, string(stateFilter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]moderation.Report, 0)
	for rows.Next() {
		rep, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rep)
	}
	return out, rows.Err()
}

// ─── Enforcements ─────────────────────────────────────────────────────────

type EnforcementRepo struct{ pool *pgxpool.Pool }

func NewEnforcementRepo(pool *pgxpool.Pool) *EnforcementRepo { return &EnforcementRepo{pool: pool} }

const enfCols = "id, account_id, rule_id, action, duration_hours, evidence_ref, decided_by, case_id, appealable, created_at, expires_at"

func scanEnforcement(row pgx.Row) (*moderation.Enforcement, error) {
	var e moderation.Enforcement
	var action string
	err := row.Scan(&e.ID, &e.AccountID, &e.RuleID, &action, &e.DurationHours, &e.EvidenceRef, &e.DecidedBy, &e.CaseID, &e.Appealable, &e.CreatedAt, &e.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, moderation.ErrEnforcementNotFound
	}
	if err != nil {
		return nil, err
	}
	e.Action = moderation.EnforcementAction(action)
	return &e, nil
}

func (r *EnforcementRepo) Create(ctx context.Context, e moderation.Enforcement) (*moderation.Enforcement, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('moderation_enforcement_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	e.ID = fmt.Sprintf("enf-%04d", seq)
	e.CaseID = fmt.Sprintf("ENF-%04d", seq)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO moderation_enforcements (id, account_id, rule_id, action, duration_hours, evidence_ref, decided_by, case_id, appealable, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), $10)
		RETURNING `+enfCols,
		e.ID, e.AccountID, e.RuleID, string(e.Action), e.DurationHours, e.EvidenceRef, e.DecidedBy, e.CaseID, e.Appealable, e.ExpiresAt)
	return scanEnforcement(row)
}

func (r *EnforcementRepo) Get(ctx context.Context, id string) (*moderation.Enforcement, error) {
	return scanEnforcement(r.pool.QueryRow(ctx, `SELECT `+enfCols+` FROM moderation_enforcements WHERE id = $1`, id))
}

func (r *EnforcementRepo) ListByAccount(ctx context.Context, accountID string) ([]moderation.Enforcement, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+enfCols+` FROM moderation_enforcements WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]moderation.Enforcement, 0)
	for rows.Next() {
		e, err := scanEnforcement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ─── Appeals ──────────────────────────────────────────────────────────────

type AppealRepo struct{ pool *pgxpool.Pool }

func NewAppealRepo(pool *pgxpool.Pool) *AppealRepo { return &AppealRepo{pool: pool} }

const appealCols = "id, enforcement_id, account_id, statement, state, reviewed_by, decided_at, case_id, created_at"

func scanAppeal(row pgx.Row) (*moderation.Appeal, error) {
	var a moderation.Appeal
	var state string
	err := row.Scan(&a.ID, &a.EnforcementID, &a.AccountID, &a.Statement, &state, &a.ReviewedBy, &a.DecidedAt, &a.CaseID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, moderation.ErrAppealNotFound
	}
	if err != nil {
		return nil, err
	}
	a.State = moderation.AppealState(state)
	return &a, nil
}

func (r *AppealRepo) HasPending(ctx context.Context, enforcementID string) (bool, error) {
	var pending bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM moderation_appeals WHERE enforcement_id = $1 AND state = 'pending')`,
		enforcementID).Scan(&pending)
	return pending, err
}

func (r *AppealRepo) Create(ctx context.Context, a moderation.Appeal) (*moderation.Appeal, error) {
	var seq int64
	if err := r.pool.QueryRow(ctx, `SELECT nextval('moderation_appeal_seq')`).Scan(&seq); err != nil {
		return nil, err
	}
	a.ID = fmt.Sprintf("appeal-%04d", seq)
	a.CaseID = fmt.Sprintf("APL-%04d", seq)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO moderation_appeals (id, enforcement_id, account_id, statement, state, case_id, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		RETURNING `+appealCols,
		a.ID, a.EnforcementID, a.AccountID, a.Statement, string(a.State), a.CaseID)
	return scanAppeal(row)
}

func (r *AppealRepo) Get(ctx context.Context, id string) (*moderation.Appeal, error) {
	return scanAppeal(r.pool.QueryRow(ctx, `SELECT `+appealCols+` FROM moderation_appeals WHERE id = $1`, id))
}

// Decide only applies when the appeal is still pending — the UPDATE's
// WHERE clause makes the check-and-write atomic in Postgres (equivalent
// to MemAppealRepo doing both under one mutex): of two concurrent
// decisions on the same appeal, exactly one UPDATE affects a row.
func (r *AppealRepo) Decide(ctx context.Context, id string, state moderation.AppealState, reviewedBy string) (*moderation.Appeal, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE moderation_appeals SET state = $2, reviewed_by = $3, decided_at = now()
		WHERE id = $1 AND state = 'pending'`,
		id, string(state), reviewedBy)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.Get(ctx, id); errors.Is(err, moderation.ErrAppealNotFound) {
			return nil, moderation.ErrAppealNotFound
		}
		return nil, moderation.ErrAppealAlreadyDecided
	}
	return r.Get(ctx, id)
}

func (r *AppealRepo) List(ctx context.Context, stateFilter moderation.AppealState) ([]moderation.Appeal, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+appealCols+` FROM moderation_appeals
		WHERE $1 = '' OR state = $1
		ORDER BY created_at DESC`, string(stateFilter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]moderation.Appeal, 0)
	for rows.Next() {
		a, err := scanAppeal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// ─── Risk profiles ────────────────────────────────────────────────────────

type RiskRepo struct{ pool *pgxpool.Pool }

func NewRiskRepo(pool *pgxpool.Pool) *RiskRepo { return &RiskRepo{pool: pool} }

func (r *RiskRepo) Get(ctx context.Context, accountID string) (*moderation.RiskProfile, error) {
	var p moderation.RiskProfile
	var status string
	err := r.pool.QueryRow(ctx, `
		SELECT account_id, risk_score, risk_reasons, review_status, updated_at
		FROM moderation_risk_profiles WHERE account_id = $1`, accountID).
		Scan(&p.AccountID, &p.RiskScore, &p.RiskReasons, &status, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return &moderation.RiskProfile{AccountID: accountID, ReviewStatus: moderation.ReviewNone, RiskReasons: []string{}}, nil
	}
	if err != nil {
		return nil, err
	}
	p.ReviewStatus = moderation.ReviewStatus(status)
	if p.RiskReasons == nil {
		p.RiskReasons = []string{}
	}
	return &p, nil
}

func (r *RiskRepo) Upsert(ctx context.Context, p moderation.RiskProfile) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO moderation_risk_profiles (account_id, risk_score, risk_reasons, review_status, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (account_id) DO UPDATE SET
			risk_score = $2, risk_reasons = $3, review_status = $4, updated_at = now()`,
		p.AccountID, p.RiskScore, p.RiskReasons, string(p.ReviewStatus))
	return err
}

func (r *RiskRepo) ListFlagged(ctx context.Context) ([]moderation.RiskProfile, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT account_id, risk_score, risk_reasons, review_status, updated_at
		FROM moderation_risk_profiles WHERE review_status <> 'none' ORDER BY risk_score DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]moderation.RiskProfile, 0)
	for rows.Next() {
		var p moderation.RiskProfile
		var status string
		if err := rows.Scan(&p.AccountID, &p.RiskScore, &p.RiskReasons, &status, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.ReviewStatus = moderation.ReviewStatus(status)
		out = append(out, p)
	}
	return out, rows.Err()
}

// ─── Moderator role set ─────────────────────────────────────────────────────

type ModeratorRepo struct{ pool *pgxpool.Pool }

func NewModeratorRepo(pool *pgxpool.Pool) *ModeratorRepo { return &ModeratorRepo{pool: pool} }

func (r *ModeratorRepo) IsModerator(ctx context.Context, accountID string) (bool, error) {
	var is bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM moderation_moderators WHERE account_id = $1)`, accountID).Scan(&is)
	return is, err
}

func (r *ModeratorRepo) Grant(ctx context.Context, accountID string) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO moderation_moderators (account_id) VALUES ($1) ON CONFLICT DO NOTHING`, accountID)
	return err
}
