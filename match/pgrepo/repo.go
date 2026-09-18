// Package pgrepo implements match.Repo against real Postgres — the
// production counterpart to match.MemRepo. The actual matching algorithm
// (candidate scoring, exclusion pool assembly) lives in matchsvc and
// never changes between backends; this package only needs to reproduce
// MemRepo's plain CRUD semantics faithfully.
package pgrepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/match"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ match.Repo = (*Repo)(nil)

func newRequestID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return "match-" + hex.EncodeToString(b)
}

const reqCols = "id, account_id, gender_preference, min_age, max_age, state, created_at, resolved_at, matched_with"

func scanRequest(row pgx.Row) (*match.Request, error) {
	var r match.Request
	var resolvedAt *time.Time
	err := row.Scan(&r.ID, &r.AccountID, &r.Preferences.GenderPreference, &r.Preferences.MinAge, &r.Preferences.MaxAge,
		&r.State, &r.CreatedAt, &resolvedAt, &r.MatchedWith)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, match.ErrRequestNotFound
	}
	if err != nil {
		return nil, err
	}
	if resolvedAt != nil {
		r.ResolvedAt = *resolvedAt
	}
	return &r, nil
}

func (r *Repo) CreateRequest(ctx context.Context, accountID string, prefs match.Preferences) (*match.Request, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO match_requests (id, account_id, gender_preference, min_age, max_age, state)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+reqCols,
		newRequestID(), accountID, prefs.GenderPreference, prefs.MinAge, prefs.MaxAge, match.StateSearching)
	return scanRequest(row)
}

func (r *Repo) GetRequest(ctx context.Context, id string) (*match.Request, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+reqCols+` FROM match_requests WHERE id = $1`, id)
	return scanRequest(row)
}

func (r *Repo) UpdateRequest(ctx context.Context, req *match.Request) error {
	var resolvedAt *time.Time
	if !req.ResolvedAt.IsZero() {
		resolvedAt = &req.ResolvedAt
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE match_requests SET
			state = $2, resolved_at = $3, matched_with = $4,
			gender_preference = $5, min_age = $6, max_age = $7
		WHERE id = $1`,
		req.ID, req.State, resolvedAt, req.MatchedWith,
		req.Preferences.GenderPreference, req.Preferences.MinAge, req.Preferences.MaxAge)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return match.ErrRequestNotFound
	}
	return nil
}

func (r *Repo) SearchingRequests(ctx context.Context, excludeAccountID string) ([]match.Request, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+reqCols+` FROM match_requests WHERE state = 'searching' AND account_id <> $1`, excludeAccountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []match.Request
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

func (r *Repo) ListHistory(ctx context.Context, accountID string) ([]match.Request, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+reqCols+` FROM match_requests WHERE account_id = $1 ORDER BY created_at DESC`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []match.Request
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *req)
	}
	return out, rows.Err()
}

func (r *Repo) RecentRequestCount(ctx context.Context, accountID string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM match_requests WHERE account_id = $1 AND created_at > $2`, accountID, since).Scan(&n)
	return n, err
}

func (r *Repo) AddCandidate(ctx context.Context, c *match.Candidate) error {
	reasons, err := json.Marshal(c.ScoreReasons)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO match_candidates (request_id, candidate_id, score, score_reasons, offered_at, decision)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (request_id, candidate_id) DO UPDATE SET
			score = $3, score_reasons = $4, offered_at = $5, decision = $6`,
		c.RequestID, c.CandidateID, c.Score, reasons, c.OfferedAt, string(c.Decision))
	return err
}

func scanCandidate(row pgx.Row) (*match.Candidate, error) {
	var c match.Candidate
	var reasons []byte
	var decision string
	err := row.Scan(&c.RequestID, &c.CandidateID, &c.Score, &reasons, &c.OfferedAt, &decision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, match.ErrCandidateNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Decision = match.Decision(decision)
	if len(reasons) > 0 {
		_ = json.Unmarshal(reasons, &c.ScoreReasons)
	}
	return &c, nil
}

const candCols = "request_id, candidate_id, score, score_reasons, offered_at, decision"

func (r *Repo) GetCandidate(ctx context.Context, requestID, candidateID string) (*match.Candidate, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+candCols+` FROM match_candidates WHERE request_id = $1 AND candidate_id = $2`, requestID, candidateID)
	return scanCandidate(row)
}

func (r *Repo) GetPendingCandidate(ctx context.Context, requestID string) (*match.Candidate, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+candCols+` FROM match_candidates
		WHERE request_id = $1 AND decision = ''
		ORDER BY offered_at DESC LIMIT 1`, requestID)
	c, err := scanCandidate(row)
	if errors.Is(err, match.ErrCandidateNotFound) {
		return nil, nil
	}
	return c, err
}

func (r *Repo) SetCandidateDecision(ctx context.Context, requestID, candidateID string, decision match.Decision) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE match_candidates SET decision = $3 WHERE request_id = $1 AND candidate_id = $2`,
		requestID, candidateID, string(decision))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return match.ErrCandidateNotFound
	}
	return nil
}

func (r *Repo) FindMirror(ctx context.Context, requestID, candidateAccountID string) (*match.Candidate, error) {
	var ownerAccountID string
	err := r.pool.QueryRow(ctx, `SELECT account_id FROM match_requests WHERE id = $1`, requestID).Scan(&ownerAccountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, match.ErrRequestNotFound
	}
	if err != nil {
		return nil, err
	}

	row := r.pool.QueryRow(ctx, `
		SELECT `+candCols+` FROM match_candidates c
		JOIN match_requests r ON r.id = c.request_id
		WHERE r.account_id = $1 AND c.candidate_id = $2
		LIMIT 1`, candidateAccountID, ownerAccountID)
	c, err := scanCandidate(row)
	if errors.Is(err, match.ErrCandidateNotFound) {
		return nil, nil
	}
	return c, err
}

func (r *Repo) AddExclusion(ctx context.Context, e match.Exclusion) error {
	var expiresAt *time.Time
	if !e.ExpiresAt.IsZero() {
		expiresAt = &e.ExpiresAt
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO match_exclusions (account_id, excluded_id, reason, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id, excluded_id) DO UPDATE SET reason = $3, expires_at = $4`,
		e.AccountID, e.ExcludedID, string(e.Reason), expiresAt)
	return err
}

func (r *Repo) IsExcluded(ctx context.Context, accountID, otherID string, now time.Time) (bool, error) {
	var excluded bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM match_exclusions
			WHERE ((account_id = $1 AND excluded_id = $2) OR (account_id = $2 AND excluded_id = $1))
				AND (expires_at IS NULL OR expires_at > $3)
		)`, accountID, otherID, now).Scan(&excluded)
	return excluded, err
}

// ─── Preferences ──────────────────────────────────────────────────────────

type PreferencesRepo struct {
	pool *pgxpool.Pool
}

func NewPreferencesRepo(pool *pgxpool.Pool) *PreferencesRepo {
	return &PreferencesRepo{pool: pool}
}

var _ match.PreferencesRepo = (*PreferencesRepo)(nil)

func (r *PreferencesRepo) Get(ctx context.Context, accountID string) (match.Preferences, error) {
	var p match.Preferences
	err := r.pool.QueryRow(ctx, `
		SELECT gender_preference, min_age, max_age FROM match_preferences WHERE account_id = $1`, accountID).
		Scan(&p.GenderPreference, &p.MinAge, &p.MaxAge)
	if errors.Is(err, pgx.ErrNoRows) {
		return match.Preferences{}, nil
	}
	return p, err
}

func (r *PreferencesRepo) Set(ctx context.Context, accountID string, prefs match.Preferences) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO match_preferences (account_id, gender_preference, min_age, max_age)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id) DO UPDATE SET gender_preference = $2, min_age = $3, max_age = $4`,
		accountID, prefs.GenderPreference, prefs.MinAge, prefs.MaxAge)
	return err
}

func (r *Repo) SkipCount(ctx context.Context, candidateID string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT r.account_id)
		FROM match_candidates c
		JOIN match_requests r ON r.id = c.request_id
		WHERE c.candidate_id = $1 AND c.decision = 'skip' AND c.offered_at >= $2`,
		candidateID, since).Scan(&n)
	return n, err
}
