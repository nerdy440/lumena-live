// Package pgrepo implements profile.Repo and profile.PrivacyRepo against
// real Postgres — the production counterpart to profile.MemProfileRepo /
// MemPrivacyRepo, matching their semantics exactly (idempotent Create,
// zero-value GetCounts/PrivacySettings for accounts with no rows yet,
// case-insensitive unique handles).
package pgrepo

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/profile"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ profile.Repo = (*Repo)(nil)

func scanProfile(row pgx.Row) (*profile.Profile, error) {
	var p profile.Profile
	err := row.Scan(&p.AccountID, &p.DisplayName, &p.Handle, &p.Bio, &p.AvatarURL,
		&p.AvatarState, &p.Languages, &p.Interests, &p.RegionCode, &p.Level, &p.IsCreator, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, profile.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

const profileCols = "account_id, display_name, handle, bio, avatar_url, avatar_state, languages, interests, region_code, level, is_creator, updated_at"

func (r *Repo) Create(ctx context.Context, accountID, displayName, regionCode string) (*profile.Profile, error) {
	if err := profile.ValidateDisplayName(displayName); err != nil {
		return nil, err
	}
	// Idempotent: a second Create for the same account returns the
	// existing row untouched — matches MemProfileRepo exactly.
	if existing, err := r.Get(ctx, accountID); err == nil {
		return existing, nil
	} else if !errors.Is(err, profile.ErrNotFound) {
		return nil, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO profiles (account_id, display_name, avatar_state, languages, interests, region_code, level)
		VALUES ($1, $2, 'pending', '{}', '{}', $3, 1)
		RETURNING `+profileCols, accountID, displayName, regionCode)
	return scanProfile(row)
}

func (r *Repo) Get(ctx context.Context, accountID string) (*profile.Profile, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+profileCols+` FROM profiles WHERE account_id = $1`, accountID)
	return scanProfile(row)
}

func (r *Repo) GetByHandle(ctx context.Context, handle string) (*profile.Profile, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+profileCols+` FROM profiles WHERE lower(handle) = lower($1)`, handle)
	return scanProfile(row)
}

func (r *Repo) Update(ctx context.Context, accountID string, req profile.UpdateRequest) (*profile.Profile, error) {
	if req.DisplayName != nil {
		if err := profile.ValidateDisplayName(*req.DisplayName); err != nil {
			return nil, err
		}
	}
	if req.Handle != nil {
		if err := profile.ValidateHandle(*req.Handle); err != nil {
			return nil, err
		}
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if req.Handle != nil {
		var existingOwner string
		err := tx.QueryRow(ctx, `SELECT account_id FROM profiles WHERE lower(handle) = lower($1)`, *req.Handle).Scan(&existingOwner)
		if err == nil && existingOwner != accountID {
			return nil, profile.ErrHandleTaken
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}

	avatarState := ""
	if req.AvatarURL != nil {
		avatarState = string(profile.AvatarPending)
	}
	languages := req.Languages
	if languages != nil {
		languages = profile.SanitizeLanguages(languages)
	}
	interests := req.Interests
	if interests != nil {
		interests = profile.SanitizeInterests(interests)
	}

	row := tx.QueryRow(ctx, `
		UPDATE profiles SET
			display_name = COALESCE($2, display_name),
			handle       = COALESCE($3, handle),
			bio          = COALESCE($4, bio),
			avatar_url   = COALESCE($5, avatar_url),
			avatar_state = CASE WHEN $5::text IS NOT NULL THEN $6 ELSE avatar_state END,
			languages    = COALESCE($7, languages),
			interests    = COALESCE($8, interests),
			region_code  = COALESCE($9, region_code),
			updated_at   = now()
		WHERE account_id = $1
		RETURNING `+profileCols,
		accountID, req.DisplayName, req.Handle, req.Bio, req.AvatarURL, nullIfEmpty(avatarState),
		languages, interests, req.RegionCode)
	p, err := scanProfile(row)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (r *Repo) GetCounts(ctx context.Context, accountID string) (*profile.ProfileCounts, error) {
	c := &profile.ProfileCounts{AccountID: accountID}
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM follows WHERE followee_id = $1),
			(SELECT COUNT(*) FROM follows WHERE follower_id = $1)
	`, accountID).Scan(&c.FollowerCount, &c.FollowingCount)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (r *Repo) Search(ctx context.Context, query string, limit int, cursor string) ([]profile.Profile, string, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, "", nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+profileCols+` FROM profiles
		WHERE account_id > $1 AND (lower(display_name) LIKE '%'||$2||'%' OR lower(COALESCE(handle,'')) LIKE '%'||$2||'%')
		ORDER BY account_id
		LIMIT $3`, cursor, q, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var results []profile.Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, "", err
		}
		results = append(results, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(results) > limit {
		results = results[:limit]
		nextCursor = results[limit-1].AccountID
	}
	return results, nextCursor, nil
}

// ─── Privacy ──────────────────────────────────────────────────────────────

type PrivacyRepo struct {
	pool *pgxpool.Pool
}

func NewPrivacyRepo(pool *pgxpool.Pool) *PrivacyRepo {
	return &PrivacyRepo{pool: pool}
}

var _ profile.PrivacyRepo = (*PrivacyRepo)(nil)

func (r *PrivacyRepo) Get(ctx context.Context, accountID string) (*profile.PrivacySettings, error) {
	var s profile.PrivacySettings
	err := r.pool.QueryRow(ctx, `
		SELECT account_id, who_can_dm, who_can_call, show_presence, discoverable, matchable
		FROM privacy_settings WHERE account_id = $1`, accountID).
		Scan(&s.AccountID, &s.WhoCanDM, &s.WhoCanCall, &s.ShowPresence, &s.Discoverable, &s.Matchable)
	if errors.Is(err, pgx.ErrNoRows) {
		def := profile.DefaultPrivacySettings(accountID)
		return &def, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *PrivacyRepo) Update(ctx context.Context, accountID string, settings profile.PrivacySettings) (*profile.PrivacySettings, error) {
	settings.AccountID = accountID
	_, err := r.pool.Exec(ctx, `
		INSERT INTO privacy_settings (account_id, who_can_dm, who_can_call, show_presence, discoverable, matchable)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (account_id) DO UPDATE SET
			who_can_dm = $2, who_can_call = $3, show_presence = $4, discoverable = $5, matchable = $6`,
		accountID, settings.WhoCanDM, settings.WhoCanCall, settings.ShowPresence, settings.Discoverable, settings.Matchable)
	if err != nil {
		return nil, err
	}
	cp := settings
	return &cp, nil
}
