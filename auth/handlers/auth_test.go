package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lumena/auth/authsvc"
	"github.com/lumena/auth/handlers"
	"github.com/lumena/auth/memrepo"
	"github.com/lumena/auth/otp"
	"github.com/lumena/auth/token"
)

func newTestHandler(t *testing.T) (*handlers.Handler, *otp.Service) {
	t.Helper()
	repo := memrepo.New()
	otpStore := otp.NewMemoryStore()
	otpSvc := otp.NewService(otpStore, &otp.LogSender{})
	issuer, _ := token.NewIssuer([]byte("test-secret-must-be-at-least-32-bytes-long-xxx"), "lumena", 15*time.Minute)
	svc := authsvc.NewService(repo, otpSvc, issuer)
	return handlers.New(svc, issuer), otpSvc
}

func post(t *testing.T, handler http.HandlerFunc, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, w.Body.String())
	}
}

// ─── Email register ───────────────────────────────────────────────────────────

func TestHandler_EmailRegister_201(t *testing.T) {
	h, _ := newTestHandler(t)

	w := post(t, h.EmailRegister, "/auth/email/register", map[string]any{
		"email":        "new@example.com",
		"password":     "ValidPass123!",
		"device_id":    "dev-001",
		"device_label": "iPhone 15",
	}, nil)

	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	decodeBody(t, w, &res)

	if res["access_token"] == "" || res["access_token"] == nil {
		t.Error("expected access_token in response")
	}
	if res["refresh_token"] == "" || res["refresh_token"] == nil {
		t.Error("expected refresh_token in response")
	}

	acc := res["account"].(map[string]any)
	if acc["is_new_account"] != true {
		t.Error("expected is_new_account=true")
	}
}

func TestHandler_EmailRegister_WeakPassword_400(t *testing.T) {
	h, _ := newTestHandler(t)

	w := post(t, h.EmailRegister, "/auth/email/register", map[string]any{
		"email":     "weak@example.com",
		"password":  "short",
		"device_id": "dev-001",
	}, nil)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}

	var res map[string]any
	decodeBody(t, w, &res)
	errBlock := res["error"].(map[string]any)
	if errBlock["code"] != "VALIDATION_ERROR" {
		t.Errorf("expected VALIDATION_ERROR code, got %v", errBlock["code"])
	}
}

func TestHandler_EmailRegister_Duplicate_409(t *testing.T) {
	h, _ := newTestHandler(t)

	body := map[string]any{
		"email": "dup@example.com", "password": "ValidPass123!", "device_id": "d",
	}
	w1 := post(t, h.EmailRegister, "/", body, nil)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first register: %d", w1.Code)
	}

	w2 := post(t, h.EmailRegister, "/", body, nil)
	if w2.Code != http.StatusConflict {
		t.Errorf("expected 409 on duplicate, got %d: %s", w2.Code, w2.Body.String())
	}
}

// ─── Email login ─────────────────────────────────────────────────────────────

func TestHandler_EmailLogin_200(t *testing.T) {
	h, _ := newTestHandler(t)

	// Register first
	post(t, h.EmailRegister, "/", map[string]any{
		"email": "login@example.com", "password": "ValidPass123!", "device_id": "d1",
	}, nil)

	w := post(t, h.EmailLogin, "/auth/email/login", map[string]any{
		"email": "login@example.com", "password": "ValidPass123!", "device_id": "d2",
	}, nil)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	decodeBody(t, w, &res)
	if res["access_token"] == nil {
		t.Error("expected access_token")
	}
}

func TestHandler_EmailLogin_WrongPassword_401(t *testing.T) {
	h, _ := newTestHandler(t)

	post(t, h.EmailRegister, "/", map[string]any{
		"email": "pw@example.com", "password": "ValidPass123!", "device_id": "d",
	}, nil)

	w := post(t, h.EmailLogin, "/", map[string]any{
		"email": "pw@example.com", "password": "WrongPass!", "device_id": "d",
	}, nil)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestHandler_EmailLogin_UnknownEmail_401(t *testing.T) {
	h, _ := newTestHandler(t)

	w := post(t, h.EmailLogin, "/", map[string]any{
		"email": "ghost@example.com", "password": "anything", "device_id": "d",
	}, nil)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	var res map[string]any
	decodeBody(t, w, &res)
	errBlock := res["error"].(map[string]any)
	// Same error message as wrong password — prevent enumeration
	if errBlock["code"] != "INVALID_CREDENTIALS" {
		t.Errorf("expected INVALID_CREDENTIALS, got %v", errBlock["code"])
	}
}

// ─── Token refresh ────────────────────────────────────────────────────────────

func TestHandler_Refresh_200(t *testing.T) {
	h, _ := newTestHandler(t)

	var regRes map[string]any
	w := post(t, h.EmailRegister, "/", map[string]any{
		"email": "refresh@example.com", "password": "ValidPass123!", "device_id": "d1",
	}, nil)
	decodeBody(t, w, &regRes)
	rt := regRes["refresh_token"].(string)

	w2 := post(t, h.Refresh, "/auth/refresh", map[string]any{
		"refresh_token": rt, "device_id": "d1",
	}, nil)

	if w2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	var res map[string]any
	decodeBody(t, w2, &res)
	if res["access_token"] == nil {
		t.Error("expected new access_token")
	}
	newRT := res["refresh_token"].(string)
	if newRT == rt {
		t.Error("refresh token must rotate")
	}
}

func TestHandler_Refresh_ReusedToken_401(t *testing.T) {
	h, _ := newTestHandler(t)

	var regRes map[string]any
	w := post(t, h.EmailRegister, "/", map[string]any{
		"email": "reuse2@example.com", "password": "ValidPass123!", "device_id": "d1",
	}, nil)
	decodeBody(t, w, &regRes)
	rt := regRes["refresh_token"].(string)

	// First refresh — rotates token
	post(t, h.Refresh, "/", map[string]any{"refresh_token": rt, "device_id": "d1"}, nil)

	// Second refresh with OLD token — reuse detected
	w2 := post(t, h.Refresh, "/", map[string]any{"refresh_token": rt, "device_id": "d1"}, nil)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on token reuse, got %d: %s", w2.Code, w2.Body.String())
	}
}

// ─── Auth middleware ──────────────────────────────────────────────────────────

func TestHandler_AuthMiddleware_ValidToken(t *testing.T) {
	h, _ := newTestHandler(t)

	var regRes map[string]any
	w := post(t, h.EmailRegister, "/", map[string]any{
		"email": "mw@example.com", "password": "ValidPass123!", "device_id": "d1",
	}, nil)
	decodeBody(t, w, &regRes)
	at := regRes["access_token"].(string)

	// Protected endpoint behind middleware
	protected := h.AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+at)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 with valid token, got %d", rec.Code)
	}
}

func TestHandler_AuthMiddleware_MissingToken_401(t *testing.T) {
	h, _ := newTestHandler(t)

	protected := h.AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_AuthMiddleware_TamperedToken_401(t *testing.T) {
	h, _ := newTestHandler(t)

	protected := h.AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJoYWNrZWQifQ.tampered")
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// ─── Age declaration ──────────────────────────────────────────────────────────

func TestHandler_DeclareAge_Adult_200(t *testing.T) {
	h, _ := newTestHandler(t)

	issuer, _ := token.NewIssuer([]byte("test-secret-must-be-at-least-32-bytes-long-xxx"), "lumena", 15*time.Minute)
	accessToken, _, _ := issuer.IssueAccessToken("acc-0001", "dev-001")

	req := httptest.NewRequest(http.MethodPost, "/auth/age/declare",
		bytes.NewBufferString(`{"dob":"1990-01-01"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()

	h.AuthMiddleware(http.HandlerFunc(h.DeclareAge)).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for adult DOB, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_DeclareAge_Underage_403(t *testing.T) {
	h, _ := newTestHandler(t)

	// Register to get a real account
	var regRes map[string]any
	w := post(t, h.EmailRegister, "/", map[string]any{
		"email": "minor2@example.com", "password": "ValidPass123!", "device_id": "d1",
	}, nil)
	decodeBody(t, w, &regRes)
	at := regRes["access_token"].(string)

	underageDOB := time.Now().AddDate(-15, 0, 0).Format("2006-01-02")
	req := httptest.NewRequest(http.MethodPost, "/auth/age/declare",
		bytes.NewBufferString(`{"dob":"`+underageDOB+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+at)
	rec := httptest.NewRecorder()

	h.AuthMiddleware(http.HandlerFunc(h.DeclareAge)).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for underage, got %d: %s", rec.Code, rec.Body.String())
	}

	var res map[string]any
	json.NewDecoder(rec.Body).Decode(&res)
	errBlock := res["error"].(map[string]any)
	if errBlock["code"] != "UNDER_AGE_MINIMUM" {
		t.Errorf("expected UNDER_AGE_MINIMUM, got %v", errBlock["code"])
	}
}

// ─── Error shape ──────────────────────────────────────────────────────────────

func TestErrorShape_AlwaysHasCodeAndMessage(t *testing.T) {
	h, _ := newTestHandler(t)

	// Every error must have code and message — no bare 500s
	w := post(t, h.EmailLogin, "/", map[string]any{
		"email": "shape@example.com", "password": "wrong", "device_id": "d",
	}, nil)

	var res map[string]any
	decodeBody(t, w, &res)

	errBlock, ok := res["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected 'error' key in response, got: %v", res)
	}
	if errBlock["code"] == nil || errBlock["code"] == "" {
		t.Error("error.code must be present and non-empty")
	}
	if errBlock["message"] == nil || errBlock["message"] == "" {
		t.Error("error.message must be present and non-empty")
	}
}
