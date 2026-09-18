package ws

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// tokenTTL matches doc 08 §1: the ws_token is short-lived (60s) and single-use.
const tokenTTL = 60 * time.Second

type tokenClaim struct {
	accountID string
	deviceID  string
	expiresAt time.Time
}

// TokenStore issues and consumes short-lived, single-use WebSocket tokens.
// It exists so the long-lived access token never has to appear in a
// WebSocket URL, which would otherwise leak into server logs and browser
// history (doc 08 §1, "Why a separate token?").
type TokenStore struct {
	mu     sync.Mutex
	tokens map[string]tokenClaim
}

func NewTokenStore() *TokenStore {
	return &TokenStore{tokens: make(map[string]tokenClaim)}
}

// Issue mints a new token scoped to accountID and deviceID.
func (s *TokenStore) Issue(accountID, deviceID string) string {
	var buf [24]byte
	_, _ = rand.Read(buf[:])
	tok := hex.EncodeToString(buf[:])

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	s.tokens[tok] = tokenClaim{
		accountID: accountID,
		deviceID:  deviceID,
		expiresAt: time.Now().Add(tokenTTL),
	}
	return tok
}

// Consume validates and single-use-consumes tok, returning the bound
// account/device. Ok is false if the token is unknown, expired, or was
// already consumed.
func (s *TokenStore) Consume(tok string) (accountID, deviceID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	claim, found := s.tokens[tok]
	if !found {
		return "", "", false
	}
	delete(s.tokens, tok) // single-use, regardless of outcome below
	if time.Now().After(claim.expiresAt) {
		return "", "", false
	}
	return claim.accountID, claim.deviceID, true
}

// gcLocked drops expired tokens. Caller must hold s.mu.
func (s *TokenStore) gcLocked() {
	now := time.Now()
	for k, v := range s.tokens {
		if now.After(v.expiresAt) {
			delete(s.tokens, k)
		}
	}
}
