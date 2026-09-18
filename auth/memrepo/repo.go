// Package memrepo provides an in-memory implementation of authsvc.Repo for tests.
package memrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lumena/auth/authsvc"
)

type MemRepo struct {
	mu       sync.RWMutex
	accounts map[string]*authsvc.Account
	byPhone  map[string]string // phone → id
	byEmail  map[string]string // email → id
	hashes   map[string]string // id → password hash
	sessions map[string]*authsvc.Session // tokenHash → session
	ages     map[string]time.Time // id → DOB
	ageStatus map[string]authsvc.AgeStatus
	nextID   int
}

func New() *MemRepo {
	return &MemRepo{
		accounts: make(map[string]*authsvc.Account),
		byPhone:  make(map[string]string),
		byEmail:  make(map[string]string),
		hashes:   make(map[string]string),
		sessions: make(map[string]*authsvc.Session),
		ages:     make(map[string]time.Time),
		ageStatus: make(map[string]authsvc.AgeStatus),
	}
}

type repoSnapshot struct {
	Accounts  map[string]*authsvc.Account `json:"accounts"`
	ByPhone   map[string]string           `json:"by_phone"`
	ByEmail   map[string]string           `json:"by_email"`
	Hashes    map[string]string           `json:"hashes"`
	Sessions  map[string]*authsvc.Session `json:"sessions"`
	Ages      map[string]time.Time        `json:"ages"`
	AgeStatus map[string]authsvc.AgeStatus `json:"age_status"`
	NextID    int                         `json:"next_id"`
}

// Snapshot/Restore back the local-persistence-to-disk feature in
// feed/cmd/api (a lightweight stand-in for a real database in this dev
// build). Password hashes ARE included deliberately — they're already
// one-way hashed, so persisting them is no different from what a real
// user-accounts table would store.
func (m *MemRepo) Snapshot() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return json.Marshal(repoSnapshot{
		Accounts: m.accounts, ByPhone: m.byPhone, ByEmail: m.byEmail, Hashes: m.hashes,
		Sessions: m.sessions, Ages: m.ages, AgeStatus: m.ageStatus, NextID: m.nextID,
	})
}

// Restore replaces this repo's entire state from a Snapshot's output. Only
// meaningful immediately after New(), before any traffic — callers must
// not mix Restore with concurrent writes.
func (m *MemRepo) Restore(data []byte) error {
	var s repoSnapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.Accounts != nil {
		m.accounts = s.Accounts
	}
	if s.ByPhone != nil {
		m.byPhone = s.ByPhone
	}
	if s.ByEmail != nil {
		m.byEmail = s.ByEmail
	}
	if s.Hashes != nil {
		m.hashes = s.Hashes
	}
	if s.Sessions != nil {
		m.sessions = s.Sessions
	}
	if s.Ages != nil {
		m.ages = s.Ages
	}
	if s.AgeStatus != nil {
		m.ageStatus = s.AgeStatus
	}
	m.nextID = s.NextID
	return nil
}

func (m *MemRepo) FindByPhone(_ context.Context, phone string) (*authsvc.Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byPhone[phone]
	if !ok {
		return nil, nil
	}
	acc := m.accounts[id]
	cp := *acc
	return &cp, nil
}

func (m *MemRepo) FindByEmail(_ context.Context, email string) (*authsvc.Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byEmail[strings.ToLower(email)]
	if !ok {
		return nil, nil
	}
	acc := m.accounts[id]
	cp := *acc
	return &cp, nil
}

func (m *MemRepo) FindByID(_ context.Context, id string) (*authsvc.Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc, ok := m.accounts[id]
	if !ok {
		return nil, nil
	}
	cp := *acc
	return &cp, nil
}

func (m *MemRepo) CreateAccount(_ context.Context, phone *string, email *string, region string) (*authsvc.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("acc-%04d", m.nextID)
	acc := &authsvc.Account{
		ID:         id,
		PhoneE164:  phone,
		Email:      email,
		Status:     authsvc.StatusActive,
		RegionCode: region,
		AgeStatus:  authsvc.AgeUndeclared,
		CreatedAt:  time.Now(),
	}
	m.accounts[id] = acc
	if phone != nil {
		m.byPhone[*phone] = id
	}
	if email != nil {
		m.byEmail[strings.ToLower(*email)] = id
	}
	m.ageStatus[id] = authsvc.AgeUndeclared
	cp := *acc
	return &cp, nil
}

func (m *MemRepo) GetPasswordHash(_ context.Context, id string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.hashes[id]
	if !ok {
		return "", errors.New("not found")
	}
	return h, nil
}

func (m *MemRepo) SetPasswordHash(_ context.Context, id, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hashes[id] = hash
	return nil
}

func (m *MemRepo) CreateSession(_ context.Context, accountID, deviceID, deviceLabel, tokenHash, ip string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sessID := fmt.Sprintf("sess-%s-%s", accountID, deviceID[:min(4, len(deviceID))])
	m.sessions[tokenHash] = &authsvc.Session{
		ID:        sessID,
		AccountID: accountID,
		DeviceID:  deviceID,
		TokenHash: tokenHash,
	}
	return sessID, nil
}

func (m *MemRepo) GetSession(_ context.Context, tokenHash string) (*authsvc.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[tokenHash]
	if !ok {
		// Also check: was this a rotated-away token that belonged to a revoked device?
		return nil, errors.New("session not found")
	}
	cp := *sess
	// If device is family-revoked, mark the copy as revoked too
	// (covers the rotated new token case)
	if cp.RevokedAt == nil && m.isDeviceRevoked(cp.AccountID, cp.DeviceID) {
		now := time.Now()
		cp.RevokedAt = &now
	}
	return &cp, nil
}

// GetActiveSession returns a session only if it is not revoked.
// Used by routes that require a live session (not the reuse detection path).
func (m *MemRepo) GetActiveSession(_ context.Context, tokenHash string) (*authsvc.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[tokenHash]
	if !ok || sess.RevokedAt != nil {
		return nil, errors.New("session not found or revoked")
	}
	cp := *sess
	return &cp, nil
}

func (m *MemRepo) RotateSession(_ context.Context, sessionID, newTokenHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for hash, sess := range m.sessions {
		if sess.ID == sessionID {
			// Mark old token as revoked (keep in map for reuse detection)
			now := time.Now()
			oldSess := &authsvc.Session{
				ID:        sess.ID,
				AccountID: sess.AccountID,
				DeviceID:  sess.DeviceID,
				TokenHash: hash,
				RevokedAt: &now,
			}
			m.sessions[hash] = oldSess
			// Store new token as active
			m.sessions[newTokenHash] = &authsvc.Session{
				ID:        sess.ID,
				AccountID: sess.AccountID,
				DeviceID:  sess.DeviceID,
				TokenHash: newTokenHash,
			}
			return nil
		}
	}
	return errors.New("session not found")
}

func (m *MemRepo) RevokeSession(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for hash, sess := range m.sessions {
		if sess.ID == sessionID {
			sess.RevokedAt = &now
			m.sessions[hash] = sess // write back — sess is a pointer, but be explicit
			return nil
		}
	}
	return nil
}

// revokedIDs tracks session IDs that have been family-revoked,
// so that even new token hashes issued for the same session are blocked.
func (m *MemRepo) isDeviceRevoked(accountID, deviceID string) bool {
	for _, sess := range m.sessions {
		if sess.AccountID == accountID && sess.DeviceID == deviceID && sess.RevokedAt != nil {
			return true
		}
	}
	return false
}

func (m *MemRepo) RevokeAllDeviceSessions(_ context.Context, accountID, deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	// Mark all matching sessions as revoked.
	// Because GetSession still returns revoked sessions (for reuse detection),
	// we need to ensure RotateSession writes are also covered.
	for _, sess := range m.sessions {
		if sess.AccountID == accountID && (deviceID == "" || sess.DeviceID == deviceID) {
			t := now
			sess.RevokedAt = &t
		}
	}
	return nil
}

func (m *MemRepo) DeclareAge(_ context.Context, id string, dob time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ages[id] = dob
	m.ageStatus[id] = authsvc.AgeDeclared
	if acc, ok := m.accounts[id]; ok {
		acc.AgeStatus = authsvc.AgeDeclared
	}
	return nil
}

func (m *MemRepo) SetAgeStatus(_ context.Context, id string, status authsvc.AgeStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ageStatus[id] = status
	if acc, ok := m.accounts[id]; ok {
		acc.AgeStatus = status
	}
	return nil
}

func (m *MemRepo) GetAgeStatus(_ context.Context, id string) (authsvc.AgeStatus, *time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.ageStatus[id]
	d, ok := m.ages[id]
	if !ok {
		return s, nil, nil
	}
	return s, &d, nil
}

func (m *MemRepo) MarkAccountDeleted(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return errors.New("not found")
	}
	acc.Status = authsvc.StatusDeleted
	return nil
}

func (m *MemRepo) SetAccountStatus(_ context.Context, id string, status authsvc.AccountStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return errors.New("not found")
	}
	acc.Status = status
	return nil
}

func (m *MemRepo) CancelDeletion(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return errors.New("not found")
	}
	if acc.Status == authsvc.StatusDeleted {
		acc.Status = authsvc.StatusActive
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
