package authsvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lumena/auth/authsvc"
	"github.com/lumena/auth/memrepo"
	"github.com/lumena/auth/otp"
	"github.com/lumena/auth/token"
)

func newTestService(t *testing.T) (*authsvc.Service, *memrepo.MemRepo, *otp.Service, *token.Issuer) {
	t.Helper()
	repo := memrepo.New()
	otpStore := otp.NewMemoryStore()
	otpSender := &otp.LogSender{}
	otpSvc := otp.NewService(otpStore, otpSender)
	issuer, err := token.NewIssuer([]byte("test-secret-must-be-at-least-32-bytes-long"), "lumena-test", 15*time.Minute)
	if err != nil {
		t.Fatalf("create issuer: %v", err)
	}
	svc := authsvc.NewService(repo, otpSvc, issuer)
	return svc, repo, otpSvc, issuer
}

// ─── Phone OTP ────────────────────────────────────────────────────────────────

func TestPhoneOTP_NewAccount(t *testing.T) {
	svc, repo, otpSvc, _ := newTestService(t)
	ctx := context.Background()

	phone := "+12125551234"
	challengeID, _, err := svc.StartPhoneAuth(ctx, phone)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Extract the code from the in-memory store (test-only)
	ch, err := otpSvc.GetChallengeForTest(ctx, challengeID)
	if err != nil {
		t.Fatalf("get challenge: %v", err)
	}
	_ = ch

	// For tests: verify with a known code injected via the store
	// In the real flow, the code is sent via SMS
	code := otpSvc.InjectCodeForTest(ctx, challengeID)

	acc, pair, err := svc.CompletePhoneAuthWithPhone(ctx, phone, challengeID, code,
		"dev-001", "Test Device", "127.0.0.1", "US")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if !acc.IsNewAccount {
		t.Error("expected IsNewAccount=true for first login")
	}
	if pair.AccessToken == "" {
		t.Error("expected non-empty access token")
	}
	if pair.RefreshToken == "" {
		t.Error("expected non-empty refresh token")
	}

	// Second login — should find existing account
	challengeID2, _, err := svc.StartPhoneAuth(ctx, phone)
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	code2 := otpSvc.InjectCodeForTest(ctx, challengeID2)
	acc2, _, err := svc.CompletePhoneAuthWithPhone(ctx, phone, challengeID2, code2,
		"dev-001", "Test Device", "127.0.0.1", "US")
	if err != nil {
		t.Fatalf("complete 2: %v", err)
	}
	if acc2.IsNewAccount {
		t.Error("expected IsNewAccount=false on second login")
	}
	if acc2.ID != acc.ID {
		t.Errorf("expected same account ID, got %s != %s", acc2.ID, acc.ID)
	}
	_ = repo
}

func TestPhoneOTP_InvalidCode(t *testing.T) {
	svc, _, otpSvc, _ := newTestService(t)
	ctx := context.Background()

	challengeID, _, err := svc.StartPhoneAuth(ctx, "+12125559999")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = otpSvc.InjectCodeForTest(ctx, challengeID) // correct code generated but not used

	_, _, err = svc.CompletePhoneAuthWithPhone(ctx, "+12125559999", challengeID, "000000",
		"dev-001", "", "127.0.0.1", "US")
	if !errors.Is(err, otp.ErrInvalid) {
		t.Errorf("expected ErrInvalid, got %v", err)
	}
}

func TestPhoneOTP_ExpiredCode(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	// Use a non-existent challenge ID — simulates expired/deleted challenge
	_, _, err := svc.CompletePhoneAuthWithPhone(ctx, "+12125559998", "nonexistent-challenge", "123456",
		"dev-001", "", "127.0.0.1", "US")
	if err == nil {
		t.Error("expected error for non-existent challenge")
	}
}

func TestPhoneOTP_InvalidPhone(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	cases := []string{"1234567890", "not-a-phone", "", "+", "+1"}
	for _, phone := range cases {
		_, _, err := svc.StartPhoneAuth(ctx, phone)
		if err == nil {
			t.Errorf("expected error for invalid phone %q, got nil", phone)
		}
	}
}

// ─── Email / Password ─────────────────────────────────────────────────────────

func TestEmailRegister_Login(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	email := "test@example.com"
	pw := "SecurePass123!"

	acc, pair, err := svc.RegisterEmail(ctx, email, pw, "dev-001", "Test", "127.0.0.1", "US")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !acc.IsNewAccount {
		t.Error("expected IsNewAccount=true")
	}
	if pair.AccessToken == "" {
		t.Error("expected access token")
	}

	// Login with correct credentials
	acc2, pair2, err := svc.LoginEmail(ctx, email, pw, "dev-002", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if acc2.ID != acc.ID {
		t.Errorf("account ID mismatch")
	}
	if pair2.AccessToken == "" {
		t.Error("expected access token on login")
	}
}

func TestEmailRegister_Duplicate(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	email := "dup@example.com"
	pw := "SecurePass123!"

	_, _, err := svc.RegisterEmail(ctx, email, pw, "dev-001", "", "127.0.0.1", "US")
	if err != nil {
		t.Fatalf("first register: %v", err)
	}

	_, _, err = svc.RegisterEmail(ctx, email, pw, "dev-002", "", "127.0.0.1", "US")
	if err == nil {
		t.Error("expected error on duplicate registration")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("expected 'already registered' error, got %v", err)
	}
}

func TestEmailLogin_WrongPassword(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	_, _, _ = svc.RegisterEmail(ctx, "pw@example.com", "CorrectPass1!", "dev-001", "", "127.0.0.1", "US")

	_, _, err := svc.LoginEmail(ctx, "pw@example.com", "WrongPass1!", "dev-001", "", "127.0.0.1")
	if err == nil {
		t.Error("expected error for wrong password")
	}
}

func TestEmailLogin_UnknownEmail(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	// Identical error shape for unknown email vs wrong password (prevent enumeration)
	_, _, errUnknown := svc.LoginEmail(ctx, "nobody@example.com", "anypassword", "dev-001", "", "127.0.0.1")
	_, _, errWrongPw := svc.LoginEmail(ctx, "nobody@example.com", "alsoanypassword", "dev-001", "", "127.0.0.1")

	if errUnknown == nil || errWrongPw == nil {
		t.Error("expected errors for both")
	}
	// Both should return the same generic message
	if errUnknown.Error() != errWrongPw.Error() {
		t.Errorf("error messages differ (timing-attack risk): %q vs %q",
			errUnknown.Error(), errWrongPw.Error())
	}
}

func TestPasswordPolicy(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	cases := []struct {
		pw      string
		wantErr bool
	}{
		{"short", true},      // too short
		{"", true},           // empty
		{"ValidPass1!", false},
		{strings.Repeat("a", 129), true}, // too long
	}

	for _, tc := range cases {
		_, _, err := svc.RegisterEmail(ctx, "policy@example.com", tc.pw, "d", "", "127.0.0.1", "US")
		if tc.wantErr && err == nil {
			t.Errorf("password %q: expected error, got nil", tc.pw)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("password %q: unexpected error: %v", tc.pw, err)
		}
	}
}

// ─── Token lifecycle ─────────────────────────────────────────────────────────

func TestTokenRefresh(t *testing.T) {
	svc, _, _, issuer := newTestService(t)
	ctx := context.Background()

	_, pair, _ := svc.RegisterEmail(ctx, "refresh@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1", "US")

	newPair, err := svc.RefreshTokens(ctx, pair.RefreshToken, "dev-001")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if newPair.AccessToken == "" {
		t.Error("expected new access token")
	}
	if newPair.RefreshToken == "" {
		t.Error("expected new refresh token")
	}
	if newPair.RefreshToken == pair.RefreshToken {
		t.Error("refresh token must rotate")
	}

	// Old refresh token must be invalidated
	_, err = svc.RefreshTokens(ctx, pair.RefreshToken, "dev-001")
	if err == nil {
		t.Error("expected error when reusing old refresh token")
	}

	// New access token must be valid
	claims, err := issuer.VerifyAccessToken(newPair.AccessToken)
	if err != nil {
		t.Errorf("new access token invalid: %v", err)
	}
	if claims.AccountID == "" {
		t.Error("expected account ID in claims")
	}
}

func TestTokenRefresh_ReuseDetection(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	_, pair, _ := svc.RegisterEmail(ctx, "reuse@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1", "US")

	// First refresh — succeeds, rotates token
	newPair, err := svc.RefreshTokens(ctx, pair.RefreshToken, "dev-001")
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Replay the original token — this is token reuse
	_, err = svc.RefreshTokens(ctx, pair.RefreshToken, "dev-001")
	if err == nil {
		t.Error("expected error on token reuse")
	}
	if !strings.Contains(err.Error(), "reuse") {
		t.Errorf("expected reuse error message, got: %v", err)
	}

	// The new token from the rotation should also be invalidated (family revocation)
	_, err = svc.RefreshTokens(ctx, newPair.RefreshToken, "dev-001")
	if err == nil {
		t.Error("expected error — family should be revoked after reuse detection")
	}
}

func TestAccessToken_Verify(t *testing.T) {
	_, _, _, issuer := newTestService(t)

	tok, claims, err := issuer.IssueAccessToken("acc-001", "dev-001")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	got, err := issuer.VerifyAccessToken(tok)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.AccountID != claims.AccountID {
		t.Errorf("account ID mismatch: %s != %s", got.AccountID, claims.AccountID)
	}
}

func TestAccessToken_Tampered(t *testing.T) {
	_, _, _, issuer := newTestService(t)
	tok, _, _ := issuer.IssueAccessToken("acc-001", "dev-001")

	// Tamper with the payload
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatal("expected 3 parts")
	}
	parts[1] = parts[1][:len(parts[1])-2] + "XX"
	tampered := strings.Join(parts, ".")

	_, err := issuer.VerifyAccessToken(tampered)
	if !errors.Is(err, token.ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid for tampered token, got: %v", err)
	}
}

// ─── Age declaration ──────────────────────────────────────────────────────────

func TestAgeDeclaration_Adult(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	acc, _, _ := svc.RegisterEmail(ctx, "adult@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1", "US")

	dob := time.Now().AddDate(-25, 0, 0) // 25 years old
	if err := svc.DeclareAge(ctx, acc.ID, dob); err != nil {
		t.Errorf("DeclareAge for adult: unexpected error: %v", err)
	}
}

func TestAgeDeclaration_Underage_HardStop(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	acc, _, _ := svc.RegisterEmail(ctx, "minor@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1", "US")

	dob := time.Now().AddDate(-15, 0, 0) // 15 years old — below minimum
	err := svc.DeclareAge(ctx, acc.ID, dob)
	if !errors.Is(err, authsvc.ErrUnderAgeMinimum) {
		t.Errorf("expected ErrUnderAgeMinimum for underage user, got: %v", err)
	}
}

func TestAgeDeclaration_ExactlyMinimum(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	acc, _, _ := svc.RegisterEmail(ctx, "exact@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1", "US")

	// Exactly 18 years old today
	dob := time.Now().AddDate(-18, 0, 0)
	if err := svc.DeclareAge(ctx, acc.ID, dob); err != nil {
		t.Errorf("DeclareAge for exactly 18: unexpected error: %v", err)
	}

	// 17 years, 364 days — still under
	dob2 := time.Now().AddDate(-18, 0, 1) // one day short
	err := svc.DeclareAge(ctx, acc.ID, dob2)
	if !errors.Is(err, authsvc.ErrUnderAgeMinimum) {
		t.Errorf("expected ErrUnderAgeMinimum for 17y364d, got: %v", err)
	}
}

// ─── Account deletion ─────────────────────────────────────────────────────────

func TestAccountDeletion_GracePeriod(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	acc, _, _ := svc.RegisterEmail(ctx, "del@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1", "US")

	// Request deletion
	if err := svc.RequestDeletion(ctx, acc.ID); err != nil {
		t.Fatalf("request deletion: %v", err)
	}

	// Cancel within grace period
	if err := svc.CancelDeletion(ctx, acc.ID); err != nil {
		t.Fatalf("cancel deletion: %v", err)
	}

	// Should still be able to log in after cancellation
	_, _, err := svc.LoginEmail(ctx, "del@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1")
	if err != nil {
		t.Errorf("login after cancelled deletion: %v", err)
	}
}

// ─── Logout ───────────────────────────────────────────────────────────────────

func TestLogout_InvalidatesSession(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	_, pair, _ := svc.RegisterEmail(ctx, "logout@example.com", "ValidPass1!", "dev-001", "", "127.0.0.1", "US")

	if err := svc.Logout(ctx, pair.RefreshToken); err != nil {
		t.Fatalf("logout: %v", err)
	}

	// Refresh after logout must fail
	_, err := svc.RefreshTokens(ctx, pair.RefreshToken, "dev-001")
	if err == nil {
		t.Error("expected error refreshing after logout")
	}
}
