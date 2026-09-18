// Package pv implements Lumena's paid 1:1 private video call — a viewer
// pays coin-per-minute to video-call another user, gated by the same
// idempotency/never-go-negative ledger discipline as every other
// money-moving feature in this codebase (gifts, premium rooms, orders).
//
// No real WebRTC SFU/TURN media server exists in this dev build — the
// gateway relays SDP/ICE over a session's pv:{id} topic (see
// gateway/ws/hub.go's relayPV), but nothing here renders actual video
// frames. Session lifecycle, gating, and billing are real; the "video"
// itself is a placeholder, exactly like streaming.DevPackagerService's
// PlaybackURL for live rooms.
package pv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// State is a session's position in its lifecycle. A session starts
// Requested, and ends at exactly one of Accepted->Ended, Declined, or
// Ended directly (caller cancels before the callee responds).
type State string

const (
	StateRequested State = "requested"
	StateAccepted  State = "accepted"
	StateDeclined  State = "declined"
	StateEnded     State = "ended"
)

// Session is one 1:1 private video call between CallerID and CalleeID.
type Session struct {
	ID              string
	CallerID        string
	CalleeID        string
	State           State
	RatePerMinCoins int64
	CreatedAt       time.Time
	AcceptedAt      *time.Time
	EndedAt         *time.Time
	// ConsumedCoins is the running total actually charged to the caller —
	// distinct from RatePerMinCoins, which is just the per-minute price.
	ConsumedCoins int64
	// HoldTransactionID is the first-minute charge posted at request time —
	// stored directly rather than looked up later, since ledger.Repo has no
	// query-by-metadata; refund/settlement reverse this exact transaction.
	HoldTransactionID string
}

// HasParticipant reports whether accountID is a party to this session —
// used to gate both REST access and the pv:{id} WebSocket topic (only the
// two participants may ever subscribe to or post on it).
func (s *Session) HasParticipant(accountID string) bool {
	return s.CallerID == accountID || s.CalleeID == accountID
}

var (
	ErrSessionNotFound = errors.New("pv: session not found")
	ErrNotParticipant  = errors.New("pv: not a participant in this session")
	ErrInvalidState    = errors.New("pv: session is not in a valid state for this action")
)

// Repo is the data-access contract for PV sessions.
type Repo interface {
	Create(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	// Update persists the given session's current field values wholesale —
	// callers read-modify-write under their own transition logic (see
	// pvsvc.Service), matching mem_ledger's "load, mutate, save" style for
	// single-row updates that don't need cross-account atomicity.
	Update(ctx context.Context, s *Session) error
	ListByAccount(ctx context.Context, accountID string) ([]Session, error)
}

// NewSessionID generates a random session ID — exported so pvsvc can mint
// one without this package needing to expose Session construction helpers
// beyond the plain struct literal.
func NewSessionID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "pv-" + hex.EncodeToString(b)
}

// ─── In-memory Repo ────────────────────────────────────────────────────────

type MemRepo struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewMemRepo() *MemRepo {
	return &MemRepo{sessions: make(map[string]*Session)}
}

var _ Repo = (*MemRepo)(nil)

func (r *MemRepo) Create(_ context.Context, s *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.sessions[s.ID] = &cp
	return nil
}

func (r *MemRepo) Get(_ context.Context, id string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, nil
	}
	cp := *s
	return &cp, nil
}

func (r *MemRepo) Update(_ context.Context, s *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[s.ID]; !ok {
		return ErrSessionNotFound
	}
	cp := *s
	r.sessions[s.ID] = &cp
	return nil
}

func (r *MemRepo) ListByAccount(_ context.Context, accountID string) ([]Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Session
	for _, s := range r.sessions {
		if s.HasParticipant(accountID) {
			out = append(out, *s)
		}
	}
	return out, nil
}
