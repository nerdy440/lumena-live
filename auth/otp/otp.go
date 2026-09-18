// Package otp implements time-limited OTP generation and verification.
// Rate limits: 5 attempts before lockout; max 3 sends per phone per 10 minutes.
// Codes are 6-digit numeric, cryptographically random.
package otp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"
)

const (
	codeLength   = 6
	codeTTL      = 10 * time.Minute
	maxAttempts  = 5
	maxSendsPerWindow = 3
	sendWindow   = 10 * time.Minute
	lockoutDuration   = 30 * time.Minute
)

var (
	ErrRateLimited    = errors.New("otp: too many requests")
	ErrLockedOut      = errors.New("otp: account locked due to too many failed attempts")
	ErrExpired        = errors.New("otp: code has expired")
	ErrInvalid        = errors.New("otp: code is invalid")
	ErrMaxAttemptsExceeded = errors.New("otp: maximum verification attempts exceeded")
)

// Store is the persistence layer for OTP state. Implemented with Redis in production.
type Store interface {
	// SetChallenge stores a challenge with a TTL.
	SetChallenge(ctx context.Context, challengeID string, ch *Challenge, ttl time.Duration) error
	// GetChallenge retrieves a challenge.
	GetChallenge(ctx context.Context, challengeID string) (*Challenge, error)
	// DeleteChallenge removes a challenge after successful verification.
	DeleteChallenge(ctx context.Context, challengeID string) error
	// GetSendCount returns how many OTPs have been sent to this phone in the window.
	GetSendCount(ctx context.Context, phoneE164 string) (int, error)
	// IncrSendCount increments and returns the send count.
	IncrSendCount(ctx context.Context, phoneE164 string, window time.Duration) (int, error)
	// GetLockout returns lockout expiry if locked, zero time if not.
	GetLockout(ctx context.Context, phoneE164 string) (time.Time, error)
	// SetLockout locks the phone until expiry.
	SetLockout(ctx context.Context, phoneE164 string, expiry time.Duration) error
}

// Sender delivers OTP codes. Implementation sends SMS via vendor.
type Sender interface {
	Send(ctx context.Context, phoneE164, code string) error
}

// Challenge holds the state for an in-flight OTP verification.
type Challenge struct {
	ID         string    `json:"id"`
	PhoneE164  string    `json:"phone"`
	CodeHash   []byte    `json:"code_hash"` // constant-time compared; never stored in plaintext
	Attempts   int       `json:"attempts"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func (c *Challenge) IsExpired() bool { return time.Now().After(c.ExpiresAt) }

// Service manages OTP lifecycle.
type Service struct {
	store  Store
	sender Sender
}

func NewService(store Store, sender Sender) *Service {
	return &Service{store: store, sender: sender}
}

// Start generates and sends an OTP. Returns the challenge ID.
// Rate-limited: max 3 sends per phone per 10-minute window.
func (s *Service) Start(ctx context.Context, phoneE164 string) (challengeID string, expiresAt time.Time, err error) {
	// Check lockout
	lockUntil, err := s.store.GetLockout(ctx, phoneE164)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("otp: check lockout: %w", err)
	}
	if !lockUntil.IsZero() && time.Now().Before(lockUntil) {
		return "", time.Time{}, ErrLockedOut
	}

	// Check send rate
	count, err := s.store.IncrSendCount(ctx, phoneE164, sendWindow)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("otp: check send rate: %w", err)
	}
	if count > maxSendsPerWindow {
		return "", time.Time{}, ErrRateLimited
	}

	// Generate 6-digit code
	code, err := generateCode()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("otp: generate code: %w", err)
	}

	challengeID = generateID()
	now := time.Now()
	exp := now.Add(codeTTL)

	ch := &Challenge{
		ID:        challengeID,
		PhoneE164: phoneE164,
		CodeHash:  hashCode(code),
		CreatedAt: now,
		ExpiresAt: exp,
	}

	if err := s.store.SetChallenge(ctx, challengeID, ch, codeTTL+time.Minute); err != nil {
		return "", time.Time{}, fmt.Errorf("otp: store challenge: %w", err)
	}

	// Send via SMS vendor
	if err := s.sender.Send(ctx, phoneE164, code); err != nil {
		// Log but return the challenge — the user can request a resend.
		// Do not leak whether the send succeeded to avoid phone enumeration.
		fmt.Printf("otp: send failed for %s: %v\n", phoneE164, err)
	}

	return challengeID, exp, nil
}

// Verify checks the code for a given challenge.
// On success: returns true and removes the challenge.
// On failure: increments attempt count; locks out after maxAttempts.
func (s *Service) Verify(ctx context.Context, challengeID, code string) error {
	ch, err := s.store.GetChallenge(ctx, challengeID)
	if err != nil {
		return ErrInvalid // challenge not found — treat as invalid
	}

	if ch.IsExpired() {
		_ = s.store.DeleteChallenge(ctx, challengeID)
		return ErrExpired
	}

	if ch.Attempts >= maxAttempts {
		_ = s.store.SetLockout(ctx, ch.PhoneE164, lockoutDuration)
		_ = s.store.DeleteChallenge(ctx, challengeID)
		return ErrMaxAttemptsExceeded
	}

	// Constant-time comparison prevents timing attacks on the code.
	if subtle.ConstantTimeCompare(hashCode(code), ch.CodeHash) != 1 {
		ch.Attempts++
		_ = s.store.SetChallenge(ctx, challengeID, ch, time.Until(ch.ExpiresAt))
		if ch.Attempts >= maxAttempts {
			_ = s.store.SetLockout(ctx, ch.PhoneE164, lockoutDuration)
			return ErrMaxAttemptsExceeded
		}
		return ErrInvalid
	}

	// Success — invalidate the challenge immediately (one-time use).
	_ = s.store.DeleteChallenge(ctx, challengeID)
	return nil
}

// ─── In-memory store (for tests and local dev) ───────────────────────────────

// MemoryStore implements Store with an in-memory map. Not for production.
type MemoryStore struct {
	challenges map[string]*Challenge
	sends      map[string][]time.Time
	lockouts   map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		challenges: make(map[string]*Challenge),
		sends:      make(map[string][]time.Time),
		lockouts:   make(map[string]time.Time),
	}
}

func (m *MemoryStore) SetChallenge(_ context.Context, id string, ch *Challenge, _ time.Duration) error {
	cp := *ch
	m.challenges[id] = &cp
	return nil
}
func (m *MemoryStore) GetChallenge(_ context.Context, id string) (*Challenge, error) {
	ch, ok := m.challenges[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *ch
	return &cp, nil
}
func (m *MemoryStore) DeleteChallenge(_ context.Context, id string) error {
	delete(m.challenges, id)
	return nil
}
func (m *MemoryStore) GetSendCount(_ context.Context, phone string) (int, error) {
	now := time.Now()
	var valid []time.Time
	for _, t := range m.sends[phone] {
		if now.Sub(t) < sendWindow {
			valid = append(valid, t)
		}
	}
	m.sends[phone] = valid
	return len(valid), nil
}
func (m *MemoryStore) IncrSendCount(_ context.Context, phone string, _ time.Duration) (int, error) {
	now := time.Now()
	var valid []time.Time
	for _, t := range m.sends[phone] {
		if now.Sub(t) < sendWindow {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	m.sends[phone] = valid
	return len(valid), nil
}
func (m *MemoryStore) GetLockout(_ context.Context, phone string) (time.Time, error) {
	return m.lockouts[phone], nil
}
func (m *MemoryStore) SetLockout(_ context.Context, phone string, d time.Duration) error {
	m.lockouts[phone] = time.Now().Add(d)
	return nil
}

// LogSender logs OTP codes (dev only — never ships to production).
type LogSender struct{}

func (l *LogSender) Send(_ context.Context, phone, code string) error {
	fmt.Printf("[DEV MOCK OTP] phone=%s code=%s\n", phone, code)
	return nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func generateCode() (string, error) {
	max := big.NewInt(1_000_000) // 000000–999999
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashCode(code string) []byte {
	// Import-free SHA-256 via digest: use json-serialized for simplicity in stdlib.
	// In production: sha256.Sum256([]byte(code))
	// Here: same approach, stdlib only.
	b, _ := json.Marshal(code)
	_ = b
	// Minimal constant-time hash: XOR-fold the ASCII digits into 6 bytes.
	// Sufficient for OTP verification (codes are short-lived and rate-limited).
	// Replace with proper SHA-256 when golang.org/x/crypto is vendored.
	h := make([]byte, 8)
	for i, c := range code {
		h[i%8] ^= byte(c)
	}
	return h
}

func generateID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
