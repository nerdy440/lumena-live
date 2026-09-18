// Package handlers implements the HTTP layer for authentication endpoints.
// All endpoints defined in doc 07 §2.
package handlers

import (
	"context"

	"github.com/lumena/shared/ctxkeys"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/lumena/auth/authsvc"
	"github.com/lumena/auth/otp"
	"github.com/lumena/auth/token"
)

// Handler holds all auth HTTP handlers.
type Handler struct {
	svc    *authsvc.Service
	issuer *token.Issuer
}

func New(svc *authsvc.Service, issuer *token.Issuer) *Handler {
	return &Handler{svc: svc, issuer: issuer}
}

// ─── Request / Response types ─────────────────────────────────────────────────

type phoneStartReq struct {
	PhoneE164 string `json:"phone_e164"`
}
type phoneStartRes struct {
	ChallengeID string    `json:"challenge_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type phoneVerifyReq struct {
	PhoneE164   string `json:"phone_e164"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
	DeviceID    string `json:"device_id"`
	DeviceLabel string `json:"device_label"`
}

type emailRegisterReq struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DeviceID    string `json:"device_id"`
	DeviceLabel string `json:"device_label"`
}

type emailLoginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	DeviceID string `json:"device_id"`
}

type refreshReq struct {
	RefreshToken string `json:"refresh_token"`
	DeviceID     string `json:"device_id"`
}

type logoutReq struct {
	RefreshToken string `json:"refresh_token"`
}

type declareDOBReq struct {
	DOB string `json:"dob"` // YYYY-MM-DD
}

type authResponse struct {
	Account      accountDTO `json:"account"`
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	ExpiresAt    time.Time  `json:"expires_at"`
}

type accountDTO struct {
	ID           string `json:"id"`
	AgeStatus    string `json:"age_status"`
	IsNewAccount bool   `json:"is_new_account"`
	Status       string `json:"status"`
}

type tokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

// POST /auth/phone/start
func (h *Handler) PhoneStart(w http.ResponseWriter, r *http.Request) {
	var req phoneStartReq
	if !decode(w, r, &req) {
		return
	}
	challengeID, expiresAt, err := h.svc.StartPhoneAuth(r.Context(), req.PhoneE164)
	if err != nil {
		if errors.Is(err, otp.ErrRateLimited) {
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests. Please wait before requesting another code.", err)
			return
		}
		if errors.Is(err, otp.ErrLockedOut) {
			writeError(w, http.StatusForbidden, "ACCOUNT_LOCKED", "Too many failed attempts. Please try again later.", err)
			return
		}
		writeError(w, http.StatusBadRequest, "INVALID_PHONE", err.Error(), err)
		return
	}
	writeJSON(w, http.StatusOK, phoneStartRes{ChallengeID: challengeID, ExpiresAt: expiresAt})
}

// POST /auth/phone/verify
func (h *Handler) PhoneVerify(w http.ResponseWriter, r *http.Request) {
	var req phoneVerifyReq
	if !decode(w, r, &req) {
		return
	}

	ip := clientIP(r)
	region := r.Header.Get("X-Region-Code")
	if region == "" {
		region = "XX"
	}

	acc, pair, err := h.svc.CompletePhoneAuthWithPhone(r.Context(),
		req.PhoneE164, req.ChallengeID, req.Code,
		req.DeviceID, req.DeviceLabel, ip, region)
	if err != nil {
		if errors.Is(err, otp.ErrExpired) {
			writeError(w, http.StatusGone, "CODE_EXPIRED", "The verification code has expired. Please request a new one.", err)
			return
		}
		if errors.Is(err, otp.ErrMaxAttemptsExceeded) {
			writeError(w, http.StatusForbidden, "MAX_ATTEMPTS_EXCEEDED", "Too many incorrect attempts. Your account has been temporarily locked.", err)
			return
		}
		if errors.Is(err, otp.ErrInvalid) {
			writeError(w, http.StatusUnprocessableEntity, "INVALID_CODE", "The verification code is incorrect.", err)
			return
		}
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Authentication failed. Please try again.", err)
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Account:      toAccountDTO(acc),
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
	})
}

// POST /auth/email/register
func (h *Handler) EmailRegister(w http.ResponseWriter, r *http.Request) {
	var req emailRegisterReq
	if !decode(w, r, &req) {
		return
	}

	ip := clientIP(r)
	region := r.Header.Get("X-Region-Code")

	acc, pair, err := h.svc.RegisterEmail(r.Context(), req.Email, req.Password, req.DeviceID, req.DeviceLabel, ip, region)
	if err != nil {
		if strings.Contains(err.Error(), "already registered") {
			writeError(w, http.StatusConflict, "ALREADY_EXISTS", "An account with this email already exists.", err)
			return
		}
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), err)
		return
	}

	writeJSON(w, http.StatusCreated, authResponse{
		Account:      toAccountDTO(acc),
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
	})
}

// POST /auth/email/login
func (h *Handler) EmailLogin(w http.ResponseWriter, r *http.Request) {
	var req emailLoginReq
	if !decode(w, r, &req) {
		return
	}

	ip := clientIP(r)
	acc, pair, err := h.svc.LoginEmail(r.Context(), req.Email, req.Password, req.DeviceID, "", ip)
	if err != nil {
		// Identical error for wrong email and wrong password (prevent enumeration).
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Incorrect email or password.", err)
		return
	}

	writeJSON(w, http.StatusOK, authResponse{
		Account:      toAccountDTO(acc),
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
	})
}

// POST /auth/refresh
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshReq
	if !decode(w, r, &req) {
		return
	}

	pair, err := h.svc.RefreshTokens(r.Context(), req.RefreshToken, req.DeviceID)
	if err != nil {
		if strings.Contains(err.Error(), "reuse detected") {
			writeError(w, http.StatusUnauthorized, "TOKEN_REUSE_DETECTED", "A security event was detected. Please log in again.", err)
			return
		}
		writeError(w, http.StatusUnauthorized, "SESSION_EXPIRED", "Your session has expired. Please log in again.", err)
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresAt:    pair.ExpiresAt,
	})
}

// POST /auth/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req logoutReq
	if !decode(w, r, &req) {
		return
	}
	_ = h.svc.Logout(r.Context(), req.RefreshToken)
	w.WriteHeader(http.StatusNoContent)
}

// POST /auth/age/declare
func (h *Handler) DeclareAge(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.", nil)
		return
	}

	var req declareDOBReq
	if !decode(w, r, &req) {
		return
	}

	dob, err := time.Parse("2006-01-02", req.DOB)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_DOB", "Date of birth must be in YYYY-MM-DD format.", err)
		return
	}

	if err := h.svc.DeclareAge(r.Context(), accountID, dob); err != nil {
		if errors.Is(err, authsvc.ErrUnderAgeMinimum) {
			// Hard stop — clear, non-ambiguous, non-recoverable.
			writeError(w, http.StatusForbidden, "UNDER_AGE_MINIMUM",
				"You must be 18 or older to use Lumena. This account has been restricted.", err)
			return
		}
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error(), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"age_status": "declared"})
}

// POST /auth/age/dev-assure — DEV ONLY, never ships to production. Stands
// in for the real age-assurance vendor integration (doc 10 §2b) that this
// local build doesn't have; see authsvc.Service.DevAssureAge's doc comment.
func (h *Handler) DevAssureAge(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.", nil)
		return
	}
	if err := h.svc.DevAssureAge(r.Context(), accountID); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not update age status.", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"age_status": "assured"})
}

// POST /me/delete
func (h *Handler) RequestDeletion(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.", nil)
		return
	}
	if err := h.svc.RequestDeletion(r.Context(), accountID); err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not start account deletion.", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"message":    "Your account will be permanently deleted in 30 days. You can cancel this during the grace period.",
		"grace_days": 30,
	})
}

// POST /me/delete/cancel
func (h *Handler) CancelDeletion(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.", nil)
		return
	}
	if err := h.svc.CancelDeletion(r.Context(), accountID); err != nil {
		writeError(w, http.StatusBadRequest, "CANCEL_FAILED", "Could not cancel deletion. The grace period may have ended.", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "Account deletion cancelled. Welcome back."})
}

// GET /auth/age/status
func (h *Handler) AgeStatus(w http.ResponseWriter, r *http.Request) {
	accountID := accountIDFromCtx(r.Context())
	if accountID == "" {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Authentication required.", nil)
		return
	}
	status, err := h.svc.GetAgeStatus(r.Context(), accountID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Could not load age status.", err)
		return
	}
	resp := map[string]string{"status": string(status)}
	if status != authsvc.AgeAssured {
		// Real vendor-based assurance (doc 10 §2b) is NOT IMPLEMENTED —
		// declaration alone (or the dev-assure escape hatch) is as far as
		// this local build can go on its own.
		resp["note"] = "Age assurance vendor integration: NOT IMPLEMENTED (Phase 0)"
	}
	writeJSON(w, http.StatusOK, resp)
}

// ─── Middleware ────────────────────────────────────────────────────────────────


// AuthMiddleware extracts and verifies the Bearer token.
// Sets account_id in context for downstream handlers.
func (h *Handler) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Bearer token required.", nil)
			return
		}

		rawToken := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := h.issuer.VerifyAccessToken(rawToken)
		if err != nil {
			if errors.Is(err, token.ErrTokenExpired) {
				writeError(w, http.StatusUnauthorized, "SESSION_EXPIRED", "Your session has expired. Please refresh your token.", nil)
				return
			}
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Invalid token.", nil)
			return
		}

		ctx := context.WithValue(r.Context(), ctxkeys.AccountID, claims.AccountID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalAuthMiddleware extracts the token if present but doesn't reject missing tokens.
// Used for guest-accessible endpoints.
func (h *Handler) OptionalAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			rawToken := strings.TrimPrefix(authHeader, "Bearer ")
			if claims, err := h.issuer.VerifyAccessToken(rawToken); err == nil {
				ctx := context.WithValue(r.Context(), ctxkeys.AccountID, claims.AccountID)
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func accountIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.AccountID).(string)
	return v
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Request body is malformed JSON.", err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string, _ error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func toAccountDTO(acc *authsvc.Account) accountDTO {
	return accountDTO{
		ID:           acc.ID,
		AgeStatus:    string(acc.AgeStatus),
		IsNewAccount: acc.IsNewAccount,
		Status:       string(acc.Status),
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		return strings.TrimSpace(parts[0])
	}
	return r.RemoteAddr
}
