// Package pgrepo implements streaming.SessionRepo against real Postgres —
// the production counterpart to streaming.MemSessionRepo.
package pgrepo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lumena/streaming"
)

type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

var _ streaming.SessionRepo = (*Repo)(nil)

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ss-" + hex.EncodeToString(b)
}

const sessionCols = `id, room_id, host_id, state, ingest_node, stream_key_id, playback_url,
	started_at, ended_at, end_reason, peak_viewers, health`

func scanSession(row pgx.Row) (*streaming.StreamSession, error) {
	var s streaming.StreamSession
	var state string
	var healthJSON []byte
	err := row.Scan(&s.ID, &s.RoomID, &s.HostID, &state, &s.IngestNode, &s.StreamKeyID, &s.PlaybackURL,
		&s.StartedAt, &s.EndedAt, &s.EndReason, &s.PeakViewers, &healthJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, streaming.ErrSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	s.State = streaming.StreamState(state)
	if len(healthJSON) > 0 {
		var h streaming.StreamHealth
		if err := json.Unmarshal(healthJSON, &h); err != nil {
			return nil, err
		}
		s.Health = &h
	}
	return &s, nil
}

func (r *Repo) Create(ctx context.Context, s streaming.StreamSession) (*streaming.StreamSession, error) {
	s.ID = newSessionID()
	row := r.pool.QueryRow(ctx, `
		INSERT INTO streaming_sessions (id, room_id, host_id, state, ingest_node, stream_key_id, playback_url,
			started_at, ended_at, end_reason, peak_viewers, health)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING `+sessionCols,
		s.ID, s.RoomID, s.HostID, string(s.State), s.IngestNode, s.StreamKeyID, s.PlaybackURL,
		s.StartedAt, s.EndedAt, s.EndReason, s.PeakViewers, healthJSONOf(s.Health))
	return scanSession(row)
}

func healthJSONOf(h *streaming.StreamHealth) []byte {
	if h == nil {
		return nil
	}
	b, _ := json.Marshal(h)
	return b
}

func (r *Repo) Get(ctx context.Context, sessionID string) (*streaming.StreamSession, error) {
	return scanSession(r.pool.QueryRow(ctx, `SELECT `+sessionCols+` FROM streaming_sessions WHERE id = $1`, sessionID))
}

func (r *Repo) GetByRoom(ctx context.Context, roomID string) (*streaming.StreamSession, error) {
	return scanSession(r.pool.QueryRow(ctx, `
		SELECT `+sessionCols+` FROM streaming_sessions WHERE room_id = $1 ORDER BY started_at DESC NULLS LAST LIMIT 1`, roomID))
}

func (r *Repo) GetActive(ctx context.Context) ([]streaming.StreamSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionCols+` FROM streaming_sessions WHERE state IN ('live', 'reconnecting')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []streaming.StreamSession
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ListByHost returns every session a host has ever broadcast, most recent
// first — not part of streaming.SessionRepo, used directly by main.go's
// creator-dashboard closure (mirrors MemSessionRepo.ListByHost).
func (r *Repo) ListByHost(ctx context.Context, hostID string) ([]streaming.StreamSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionCols+` FROM streaming_sessions WHERE host_id = $1 ORDER BY id DESC`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []streaming.StreamSession
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ListAll returns every session ever created — not part of
// streaming.SessionRepo, used directly by main.go's analytics closure
// (mirrors MemSessionRepo.ListAll).
func (r *Repo) ListAll(ctx context.Context) ([]streaming.StreamSession, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sessionCols+` FROM streaming_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []streaming.StreamSession
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *Repo) UpdateState(ctx context.Context, sessionID string, state streaming.StreamState, reason string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE streaming_sessions SET
			state = $2,
			started_at = CASE WHEN $2 = 'live' AND started_at IS NULL THEN now() ELSE started_at END,
			ended_at = CASE WHEN $2 IN ('ended', 'terminated') AND ended_at IS NULL THEN now() ELSE ended_at END,
			end_reason = CASE WHEN $2 IN ('ended', 'terminated') AND ended_at IS NULL THEN $3 ELSE end_reason END
		WHERE id = $1`,
		sessionID, string(state), reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return streaming.ErrSessionNotFound
	}
	return nil
}

func (r *Repo) UpdateHealth(ctx context.Context, sessionID string, h streaming.StreamHealth) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `UPDATE streaming_sessions SET health = $2 WHERE id = $1`, sessionID, b)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return streaming.ErrSessionNotFound
	}
	return nil
}

func (r *Repo) End(ctx context.Context, sessionID string, reason string) error {
	return r.UpdateState(ctx, sessionID, streaming.StateEnded, reason)
}

func (r *Repo) UpdatePlaybackURL(ctx context.Context, sessionID, url string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE streaming_sessions SET playback_url = $2 WHERE id = $1`, sessionID, url)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return streaming.ErrSessionNotFound
	}
	return nil
}
