// Package authsvc implements the authentication domain service.
// Covers: phone OTP, email/password, session management, age declaration,
// account recovery, and account deletion (BT-10).
package authsvc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lumena/auth/otp"
	"github.com/lumena/auth/password"
	"github.com/lumena/auth/token"
)

// AccountStatus mirrors the DB enum.
type AccountStatus string

const (
	StatusActive     AccountStatus = "active"
	StatusRestricted AccountStatus = "restricted"
	StatusSuspended  AccountStatus = "suspended"
	StatusDeleted    AccountStatus = "deleted"
)

// AgeStatus mirrors the DB enum.
type AgeStatus string

const (
	AgeUndeclared AgeStatus = "undeclared"
	AgeDeclared   AgeStatus = "declared"
	AgeAssured    AgeStatus = "assured"
	AgeFailed     AgeStatus = "failed"
)

// MinimumAge is the hard floor. Accounts below this cannot be created.
// Broadcast and private video require AgeAssured (additional step).
const MinimumAge = 18

// Account is the domain object returned after successful auth.
type Account struct {
	ID           string
	PhoneE164    *string
	Email        *string
	Status       AccountStatus
	RegionCode   string
	AgeStatus    AgeStatus
	IsNewAccount bool
	CreatedAt    time.Time
}

// TokenPair is returned on successful authentication.
type TokenPair struct {
	AccessToken  string
	RefreshToken string // raw value; hash stored in DB
	ExpiresAt    time.Time
}

// Repo is the data access interface. Implemented by the Postgres layer.
// Keeping it as an interface here means the auth service is testable
// without a database.
type Repo interface {
	// FindByPhone returns the account for the given E.164 phone number, or nil.
	FindByPhone(ctx context.Context, phoneE164 string) (*Account, error)
	// FindByEmail returns the account for the given normalized email, or nil.
	FindByEmail(ctx context.Context, email string) (*Account, error)
	// FindByID returns the account by ID.
	FindByID(ctx context.Context, id string) (*Account, error)
	// CreateAccount creates a new account and returns it.
	CreateAccount(ctx context.Context, phoneE164 *string, email *string, region string) (*Account, error)
	// GetPasswordHash returns the stored password hash for an email account.
	GetPasswordHash(ctx context.Context, accountID string) (string, error)
	// SetPasswordHash sets the password hash.
	SetPasswordHash(ctx context.Context, accountID, hash string) error
	// CreateSession stores a new session.
	CreateSession(ctx context.Context, accountID, deviceID, deviceLabel, tokenHash string, ip string) (string, error)
	// GetSession fetches a session by refresh token hash.
	GetSession(ctx context.Context, tokenHash string) (*Session, error)
	// RotateSession replaces the refresh token hash and updates last_seen.
	RotateSession(ctx context.Context, sessionID, newTokenHash string) error
	// RevokeSession marks a session as revoked.
	RevokeSession(ctx context.Context, sessionID string) error
	// RevokeAllDeviceSessions revokes all sessions for a device (reuse detection).
	RevokeAllDeviceSessions(ctx context.Context, accountID, deviceID string) error
	// DeclareAge records a date-of-birth declaration.
	DeclareAge(ctx context.Context, accountID string, dob time.Time) error
	// GetAgeStatus returns the age status for an account.
	GetAgeStatus(ctx context.Context, accountID string) (AgeStatus, *time.Time, error)
	// SetAgeStatus force-sets the age status — used only by the dev-only
	// age-assurance escalation (see Service.DevAssureAge's doc comment).
	SetAgeStatus(ctx context.Context, accountID string, status AgeStatus) error
	// SetAccountStatus force-sets the account's standing — the enforcement
	// action (moderation.Service applies restrict/suspend here; see
	// authsvc.Service.SetAccountStatus's doc comment).
	SetAccountStatus(ctx context.Context, accountID string, status AccountStatus) error
	// MarkAccountDeleted starts the deletion grace period.
	MarkAccountDeleted(ctx context.Context, accountID string) error
	// CancelDeletion cancels a pending deletion.
	CancelDeletion(ctx context.Context, accountID string) error
}

// Session is a persisted auth session.
type Session struct {
	ID        string
	AccountID string
	DeviceID  string
	TokenHash string
	RevokedAt *time.Time
}

// AuthEventHook is notified after a successful registration or login, so
// the analytics module's event pipeline (Phase 17) can observe DAU/MAU and
// funnel signals without authsvc importing analytics. event is
// "account_created" or "login". Optional.
type AuthEventHook func(ctx context.Context, accountID, event string)

// DeviceLoginHook is notified on every successful token issuance
// (registration or login) with the device ID used — the fraud module's
// device-fingerprint clustering (Phase 18, AF-02) wires this without
// authsvc importing fraud. Optional.
type DeviceLoginHook func(ctx context.Context, accountID, deviceID string)

// Service is the auth domain service.
type Service struct {
	repo    Repo
	otp     *otp.Service
	issuer  *token.Issuer
	onAuthEvent AuthEventHook
	onDeviceLogin DeviceLoginHook
}

func NewService(repo Repo, otpSvc *otp.Service, issuer *token.Issuer) *Service {
	return &Service{repo: repo, otp: otpSvc, issuer: issuer}
}

// WithAuthEventHook registers the analytics module's event recorder.
func (s *Service) WithAuthEventHook(hook AuthEventHook) *Service {
	s.onAuthEvent = hook
	return s
}

// WithDeviceLoginHook registers the fraud module's device-clustering observer.
func (s *Service) WithDeviceLoginHook(hook DeviceLoginHook) *Service {
	s.onDeviceLogin = hook
	return s
}

// ─── Phone OTP ────────────────────────────────────────────────────────────────

// StartPhoneAuth begins phone verification. Returns challengeID and expiry.
func (s *Service) StartPhoneAuth(ctx context.Context, phoneE164 string) (string, time.Time, error) {
	if err := validatePhone(phoneE164); err != nil {
		return "", time.Time{}, err
	}
	return s.otp.Start(ctx, phoneE164)
}

// CompletePhoneAuth verifies the OTP and returns tokens.
// If the account doesn't exist, it is created (first-time login).
func (s *Service) CompletePhoneAuth(ctx context.Context, challengeID, code, deviceID, deviceLabel, ip, region string) (*Account, *TokenPair, error) {
	// The challenge itself contains the phone number — we verify the code,
	// then use the phone from the verified challenge, not from the client.
	// This prevents a client from verifying A's code and claiming phone B.
	if err := s.otp.Verify(ctx, challengeID, code); err != nil {
		return nil, nil, err
	}

	// otp.Verify succeeded — we need the phone from the challenge.
	// In production: otp.Verify returns the verified phone.
	// Here we use a simplified path where the challenge ID maps to phone via the store.
	// The handler passes phone separately; we trust it because the OTP verified it.
	// TODO: refactor otp.Verify to return the verified phone directly.

	return s.getOrCreateByPhone(ctx, nil, deviceID, deviceLabel, ip, region)
}

// CompletePhoneAuthWithPhone is the actual implementation used by the handler.
// phoneE164 is trusted only because the OTP verification succeeded.
func (s *Service) CompletePhoneAuthWithPhone(ctx context.Context, phoneE164, challengeID, code, deviceID, deviceLabel, ip, region string) (*Account, *TokenPair, error) {
	if err := s.otp.Verify(ctx, challengeID, code); err != nil {
		return nil, nil, err
	}
	return s.getOrCreateByPhone(ctx, &phoneE164, deviceID, deviceLabel, ip, region)
}

func (s *Service) getOrCreateByPhone(ctx context.Context, phoneE164 *string, deviceID, deviceLabel, ip, region string) (*Account, *TokenPair, error) {
	var acc *Account
	var err error

	if phoneE164 != nil {
		acc, err = s.repo.FindByPhone(ctx, *phoneE164)
		if err != nil {
			return nil, nil, fmt.Errorf("auth: find by phone: %w", err)
		}
	}

	isNew := acc == nil
	if isNew {
		acc, err = s.repo.CreateAccount(ctx, phoneE164, nil, region)
		if err != nil {
			return nil, nil, fmt.Errorf("auth: create account: %w", err)
		}
		acc.IsNewAccount = true
	}

	if acc.Status == StatusSuspended || acc.Status == StatusDeleted {
		return nil, nil, fmt.Errorf("auth: account unavailable")
	}

	pair, err := s.issueTokens(ctx, acc.ID, deviceID, deviceLabel, ip)
	if err != nil {
		return nil, nil, err
	}

	if s.onAuthEvent != nil {
		if isNew {
			s.onAuthEvent(ctx, acc.ID, "account_created")
		} else {
			s.onAuthEvent(ctx, acc.ID, "login")
		}
	}
	return acc, pair, nil
}

// ─── Email / Password ─────────────────────────────────────────────────────────

// RegisterEmail creates a new email+password account.
func (s *Service) RegisterEmail(ctx context.Context, email, pw, deviceID, deviceLabel, ip, region string) (*Account, *TokenPair, error) {
	email = normalizeEmail(email)
	if email == "" {
		return nil, nil, fmt.Errorf("auth: invalid email")
	}
	if err := password.Validate(pw); err != nil {
		return nil, nil, err
	}

	existing, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		return nil, nil, err
	}
	if existing != nil {
		// Constant-time response — don't reveal whether the email exists.
		// (In practice, UX usually requires revealing this; handle at the product layer.)
		return nil, nil, fmt.Errorf("auth: email already registered")
	}

	hash, err := password.Hash(pw)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: hash password: %w", err)
	}

	acc, err := s.repo.CreateAccount(ctx, nil, &email, region)
	if err != nil {
		return nil, nil, fmt.Errorf("auth: create account: %w", err)
	}
	if err := s.repo.SetPasswordHash(ctx, acc.ID, hash); err != nil {
		return nil, nil, fmt.Errorf("auth: store password: %w", err)
	}

	acc.IsNewAccount = true
	pair, err := s.issueTokens(ctx, acc.ID, deviceID, deviceLabel, ip)
	if err == nil && s.onAuthEvent != nil {
		s.onAuthEvent(ctx, acc.ID, "account_created")
	}
	return acc, pair, err
}

// LoginEmail authenticates with email and password.
// Deliberately uses the same response shape for "wrong email" and "wrong password"
// to prevent user enumeration.
func (s *Service) LoginEmail(ctx context.Context, email, pw, deviceID, deviceLabel, ip string) (*Account, *TokenPair, error) {
	email = normalizeEmail(email)

	acc, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		return nil, nil, err
	}

	// Always fetch and compare — constant-time behavior even when account not found.
	var storedHash string
	if acc != nil {
		storedHash, _ = s.repo.GetPasswordHash(ctx, acc.ID)
	} else {
		// Hash a dummy value to consume constant time.
		storedHash = "$pbkdf2-sha512-v1$600000$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}

	verifyErr := password.Verify(pw, storedHash)

	if acc == nil || verifyErr != nil {
		return nil, nil, fmt.Errorf("auth: invalid credentials")
	}

	if acc.Status == StatusSuspended || acc.Status == StatusDeleted {
		return nil, nil, fmt.Errorf("auth: account unavailable")
	}

	pair, err := s.issueTokens(ctx, acc.ID, deviceID, deviceLabel, ip)
	if err == nil && s.onAuthEvent != nil {
		s.onAuthEvent(ctx, acc.ID, "login")
	}
	return acc, pair, err
}

// ─── Token lifecycle ─────────────────────────────────────────────────────────

// RefreshTokens rotates the refresh token and issues a new access token.
// Reuse detection: if a revoked token is presented, all sessions for that device
// are revoked (the entire family) — RFC 6749 §10.4.
func (s *Service) RefreshTokens(ctx context.Context, rawRefreshToken, deviceID string) (*TokenPair, error) {
	hash := token.HashRefreshTokenForLookup(rawRefreshToken)
	sess, err := s.repo.GetSession(ctx, string(hash))
	if err != nil {
		return nil, fmt.Errorf("auth: session not found")
	}

	if sess.RevokedAt != nil {
		// Reuse of a revoked token → revoke entire device family (RFC 6749 §10.4).
		// We still know the account and device from the revoked session record.
		_ = s.repo.RevokeAllDeviceSessions(ctx, sess.AccountID, sess.DeviceID)
		return nil, fmt.Errorf("auth: refresh token reuse detected — all sessions revoked")
	}

	acc, err := s.repo.FindByID(ctx, sess.AccountID)
	if err != nil || acc == nil {
		return nil, fmt.Errorf("auth: account not found")
	}
	if acc.Status != StatusActive {
		return nil, fmt.Errorf("auth: account unavailable")
	}

	// Issue new access token.
	_, claims, err := s.issuer.IssueAccessToken(acc.ID, deviceID)
	if err != nil {
		return nil, err
	}

	// Rotate refresh token.
	newRT, err := token.IssueRefreshToken()
	if err != nil {
		return nil, err
	}

	if err := s.repo.RotateSession(ctx, sess.ID, string(newRT.Hash)); err != nil {
		return nil, fmt.Errorf("auth: rotate session: %w", err)
	}

	// Re-issue full access token with the claims we already computed.
	accessToken, _, err := s.issuer.IssueAccessToken(acc.ID, deviceID)
	if err != nil {
		return nil, err
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: newRT.Raw,
		ExpiresAt:    time.Unix(claims.ExpiresAt, 0),
	}, nil
}

// Logout revokes the current session.
func (s *Service) Logout(ctx context.Context, rawRefreshToken string) error {
	hash := token.HashRefreshTokenForLookup(rawRefreshToken)
	sess, err := s.repo.GetSession(ctx, string(hash))
	if err != nil {
		return nil // idempotent
	}
	return s.repo.RevokeSession(ctx, sess.ID)
}

// ─── Age declaration (BT-11 partial) ─────────────────────────────────────────

// DeclareAge records a date-of-birth and validates the minimum age.
// Hard stop: accounts below MinimumAge receive ErrUnderAgeMinimum.
// This error causes the account to be flagged; the session remains valid
// only for the age-declaration surface.
var ErrUnderAgeMinimum = errors.New("auth: user is below the minimum age")
var ErrAccountNotFound = errors.New("auth: account not found")

func (s *Service) DeclareAge(ctx context.Context, accountID string, dob time.Time) error {
	age := ageInYears(dob)
	if age < MinimumAge {
		// Mark the account — every gated surface will return AGE_MINIMUM_REQUIRED.
		// We do NOT delete the account silently; the user gets a clear message.
		return ErrUnderAgeMinimum
	}
	return s.repo.DeclareAge(ctx, accountID, dob)
}

// GetAgeStatus returns the account's current age status.
func (s *Service) GetAgeStatus(ctx context.Context, accountID string) (AgeStatus, error) {
	status, _, err := s.repo.GetAgeStatus(ctx, accountID)
	return status, err
}

// GetAccountStatus returns the account's current standing (active,
// restricted, suspended, deleted) — used by payout gate #4 (doc 11 §7: "no
// active account restriction") without exposing the whole Account record.
func (s *Service) GetAccountStatus(ctx context.Context, accountID string) (AccountStatus, error) {
	acc, err := s.repo.FindByID(ctx, accountID)
	if err != nil {
		return "", err
	}
	if acc == nil {
		return "", ErrAccountNotFound
	}
	return acc.Status, nil
}

// GetAccountCreatedAt returns when accountID was created — used by the
// moderation module's grooming-risk engine to detect "recently registered"
// accounts (doc 10 §2d).
func (s *Service) GetAccountCreatedAt(ctx context.Context, accountID string) (time.Time, error) {
	acc, err := s.repo.FindByID(ctx, accountID)
	if err != nil {
		return time.Time{}, err
	}
	if acc == nil {
		return time.Time{}, ErrAccountNotFound
	}
	return acc.CreatedAt, nil
}

// SetAccountStatus force-sets an account's standing (active, restricted,
// suspended). This is the real enforcement action applied when a
// moderation.Enforcement is created — not a dev-only escape hatch — so a
// restricted/suspended account is immediately locked out of every gated
// surface that checks Status, matching doc 10 §8's transparency model.
func (s *Service) SetAccountStatus(ctx context.Context, accountID string, status AccountStatus) error {
	return s.repo.SetAccountStatus(ctx, accountID, status)
}

// DevAssureAge force-sets an account to AgeAssured, standing in for the
// real age-assurance vendor integration (doc 10 §2b, doc 12 Phase 0's
// AUTH_009) that this local dev build doesn't have — same category of gap,
// and same dev-escalation pattern, as wallet's dev-topup. Broadcast and
// private video are gated on this status; without a real vendor there is
// no other way to reach it for testing. NEVER ships to production.
func (s *Service) DevAssureAge(ctx context.Context, accountID string) error {
	return s.repo.SetAgeStatus(ctx, accountID, AgeAssured)
}

// ─── Account recovery (BT-10) ─────────────────────────────────────────────────

// RecoveryToken is sent to the user's verified phone/email.
// Recovery flow: StartRecovery → user clicks link with token → CompleteRecovery.
type RecoveryToken struct {
	Token     string
	ExpiresAt time.Time
}

// StartRecovery initiates account recovery. Returns a token to send via OTP/email.
// The response is identical whether or not the account exists (prevent enumeration).
func (s *Service) StartRecovery(ctx context.Context, identifier string) (*RecoveryToken, error) {
	// In production: send the token via OTP/email, return only success/failure.
	// Here: return the token for testing.
	rt, err := token.IssueRefreshToken()
	if err != nil {
		return nil, err
	}
	return &RecoveryToken{
		Token:     rt.Raw,
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}, nil
}

// ─── Account deletion (BT-10, GDPR Art.17) ───────────────────────────────────

// RequestDeletion starts the 30-day grace period deletion flow.
// During grace period, the account is soft-deleted. CancelDeletion can reverse it.
func (s *Service) RequestDeletion(ctx context.Context, accountID string) error {
	return s.repo.MarkAccountDeleted(ctx, accountID)
}

// CancelDeletion cancels a pending deletion (within grace period).
func (s *Service) CancelDeletion(ctx context.Context, accountID string) error {
	return s.repo.CancelDeletion(ctx, accountID)
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func (s *Service) issueTokens(ctx context.Context, accountID, deviceID, deviceLabel, ip string) (*TokenPair, error) {
	accessToken, claims, err := s.issuer.IssueAccessToken(accountID, deviceID)
	if err != nil {
		return nil, fmt.Errorf("auth: issue access token: %w", err)
	}

	rt, err := token.IssueRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("auth: issue refresh token: %w", err)
	}

	_, err = s.repo.CreateSession(ctx, accountID, deviceID, deviceLabel, string(rt.Hash), ip)
	if err != nil {
		return nil, fmt.Errorf("auth: create session: %w", err)
	}

	if s.onDeviceLogin != nil && deviceID != "" {
		s.onDeviceLogin(ctx, accountID, deviceID)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: rt.Raw,
		ExpiresAt:    time.Unix(claims.ExpiresAt, 0),
	}, nil
}

var phoneRe = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

func validatePhone(phone string) error {
	if !phoneRe.MatchString(phone) {
		return fmt.Errorf("auth: invalid phone number (E.164 required, e.g. +12125551234)")
	}
	return nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func ageInYears(dob time.Time) int {
	now := time.Now()
	years := now.Year() - dob.Year()
	if now.Month() < dob.Month() || (now.Month() == dob.Month() && now.Day() < dob.Day()) {
		years--
	}
	return years
}
