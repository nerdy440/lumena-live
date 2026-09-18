// Package token implements access and refresh token issuance and verification.
// Uses HMAC-SHA256 (stdlib only, no external JWT library required for core logic).
// Production upgrade path: swap Sign/Verify for RS256 by changing the signer interface.
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims are the payload embedded in an access token.
type Claims struct {
	AccountID  string `json:"sub"`
	DeviceID   string `json:"did"`
	IssuedAt   int64  `json:"iat"`
	ExpiresAt  int64  `json:"exp"`
	Issuer     string `json:"iss"`
}

// RefreshToken is an opaque, high-entropy random token.
// Stored as its SHA-512 hash in the DB; the raw value is returned to the client once.
type RefreshToken struct {
	Raw  string // returned to client
	Hash []byte // stored in DB
}

var (
	ErrTokenExpired   = errors.New("token expired")
	ErrTokenInvalid   = errors.New("token invalid")
	ErrTokenMalformed = errors.New("token malformed")
)

// Issuer creates and verifies tokens.
type Issuer struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
}

// NewIssuer creates a token issuer.
// secret must be at least 32 bytes; load from secrets manager, never from env in prod.
func NewIssuer(secret []byte, issuer string, accessTTL time.Duration) (*Issuer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("token: secret must be at least 32 bytes")
	}
	if accessTTL <= 0 {
		accessTTL = 15 * time.Minute
	}
	return &Issuer{secret: secret, issuer: issuer, accessTTL: accessTTL}, nil
}

// IssueAccessToken creates a signed access token.
func (is *Issuer) IssueAccessToken(accountID, deviceID string) (string, *Claims, error) {
	now := time.Now()
	claims := &Claims{
		AccountID: accountID,
		DeviceID:  deviceID,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(is.accessTTL).Unix(),
		Issuer:    is.issuer,
	}

	header := base64url(mustJSON(map[string]string{"alg": "HS256", "typ": "JWT"}))
	payload := base64url(mustJSON(claims))
	unsigned := header + "." + payload
	sig := is.sign([]byte(unsigned))

	return unsigned + "." + base64url(sig), claims, nil
}

// VerifyAccessToken verifies and parses an access token.
func (is *Issuer) VerifyAccessToken(raw string) (*Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, ErrTokenMalformed
	}

	unsigned := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrTokenMalformed
	}

	expected := is.sign([]byte(unsigned))
	if !hmac.Equal(sig, expected) {
		return nil, ErrTokenInvalid
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrTokenMalformed
	}

	var claims Claims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, ErrTokenMalformed
	}

	if time.Now().Unix() > claims.ExpiresAt {
		return nil, ErrTokenExpired
	}
	if claims.Issuer != is.issuer {
		return nil, ErrTokenInvalid
	}

	return &claims, nil
}

// IssueRefreshToken generates a cryptographically random refresh token.
// The raw value is returned to the client. The hash is stored in the DB.
func IssueRefreshToken() (*RefreshToken, error) {
	b := make([]byte, 48)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("token: generate refresh: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	hash := hashRefreshToken(raw)
	return &RefreshToken{Raw: raw, Hash: hash}, nil
}

// HashRefreshToken returns the stored representation of a refresh token.
func hashRefreshToken(raw string) []byte {
	h := sha512.Sum512([]byte(raw))
	return h[:]
}

// HashRefreshTokenForLookup is the public version for verifying tokens at login time.
func HashRefreshTokenForLookup(raw string) []byte {
	return hashRefreshToken(raw)
}

func (is *Issuer) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, is.secret)
	mac.Write(data)
	return mac.Sum(nil)
}

func base64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
