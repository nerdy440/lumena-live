package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lumena/profile"
	"github.com/lumena/profile/handlers"
	"github.com/lumena/profile/profilesvc"
	"github.com/lumena/profile/social"
)

func newHandler(t *testing.T) (*handlers.Handler, *profilesvc.Service) {
	t.Helper()
	profileRepo := profile.NewMemProfileRepo()
	privacyRepo := profile.NewMemPrivacyRepo()
	socialRepo := social.NewMemSocialRepo()
	svc := profilesvc.NewService(profileRepo, privacyRepo, socialRepo)

	// Pre-seed two accounts
	svc.CreateProfile(context.Background(), "acc-alice", "Alice", "US")
	svc.CreateProfile(context.Background(), "acc-bob", "Bob", "UK")

	return handlers.New(svc), svc
}

func authed(r *http.Request, accountID string) *http.Request {
	return r.WithContext(handlers.WithAccountID(r.Context(), accountID))
}

func doRequest(handler http.HandlerFunc, method, path string, body any, accountID string) *httptest.ResponseRecorder {
	var bodyReader *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	if accountID != "" {
		req = authed(req, accountID)
	}
	w := httptest.NewRecorder()
	handler(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(dst); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, w.Body.String())
	}
}

// ─── GET /users/{id} ─────────────────────────────────────────────────────────

func TestGetProfile_200(t *testing.T) {
	h, _ := newHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/users/acc-bob", nil)
	req.SetPathValue("id", "acc-bob")
	req = authed(req, "acc-alice")
	w := httptest.NewRecorder()
	h.GetProfile(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	decode(t, w, &res)
	p := res["profile"].(map[string]any)
	if p["display_name"] != "Bob" {
		t.Errorf("expected Bob, got %v", p["display_name"])
	}
}

func TestGetProfile_Blocked_404(t *testing.T) {
	h, svc := newHandler(t)
	ctx := context.Background()

	// Bob blocks alice
	svc.Block(ctx, "acc-bob", "acc-alice")

	req := httptest.NewRequest(http.MethodGet, "/users/acc-bob", nil)
	req.SetPathValue("id", "acc-bob")
	req = authed(req, "acc-alice")
	w := httptest.NewRecorder()
	h.GetProfile(w, req)

	// Must return 404 — identical to not-found; does not reveal block (doc 04 §3)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 when blocked, got %d", w.Code)
	}

	var res map[string]any
	decode(t, w, &res)
	errBlock := res["error"].(map[string]any)
	if errBlock["code"] != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND code (not a block-specific code), got %v", errBlock["code"])
	}
}

func TestGetMyProfile_200(t *testing.T) {
	h, _ := newHandler(t)
	w := doRequest(h.GetMyProfile, http.MethodGet, "/me", nil, "acc-alice")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var res map[string]any
	decode(t, w, &res)
	if res["is_owner"] != true {
		t.Error("expected is_owner=true for own profile")
	}
}

func TestGetMyProfile_NoAuth_401(t *testing.T) {
	h, _ := newHandler(t)
	w := doRequest(h.GetMyProfile, http.MethodGet, "/me", nil, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ─── PATCH /me ───────────────────────────────────────────────────────────────

func TestUpdateProfile_200(t *testing.T) {
	h, _ := newHandler(t)

	handle := "alice_live"
	bio := "Hello world"
	w := doRequest(h.UpdateProfile, http.MethodPatch, "/me", map[string]any{
		"handle": handle,
		"bio":    bio,
	}, "acc-alice")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	decode(t, w, &res)
	if res["handle"] != handle {
		t.Errorf("expected handle %q, got %v", handle, res["handle"])
	}
}

func TestUpdateProfile_HandleTaken_409(t *testing.T) {
	h, svc := newHandler(t)
	ctx := context.Background()

	handle := "shared_handle"
	svc.UpdateProfile(ctx, "acc-alice", profile.UpdateRequest{Handle: &handle})

	w := doRequest(h.UpdateProfile, http.MethodPatch, "/me", map[string]any{
		"handle": handle,
	}, "acc-bob")

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 for taken handle, got %d: %s", w.Code, w.Body.String())
	}
}

// ─── POST /follows ────────────────────────────────────────────────────────────

func TestFollow_200(t *testing.T) {
	h, _ := newHandler(t)

	w := doRequest(h.Follow, http.MethodPost, "/follows", map[string]any{
		"followee_id": "acc-bob",
	}, "acc-alice")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	decode(t, w, &res)
	rel := res["relationship"].(map[string]any)
	if rel["following"] != true {
		t.Error("expected following=true in response")
	}
}

// TestUnfollow_200 — BT-02: unfollow is a dedicated DELETE endpoint, never requires block
func TestUnfollow_200(t *testing.T) {
	h, svc := newHandler(t)
	ctx := context.Background()

	// Alice follows bob
	svc.Follow(ctx, "acc-alice", "acc-bob")

	req := httptest.NewRequest(http.MethodDelete, "/follows/acc-bob", nil)
	req.SetPathValue("followee_id", "acc-bob")
	req = authed(req, "acc-alice")
	w := httptest.NewRecorder()
	h.Unfollow(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("BT-02: expected 200 on direct unfollow, got %d: %s", w.Code, w.Body.String())
	}

	var res map[string]any
	decode(t, w, &res)
	rel := res["relationship"].(map[string]any)
	if rel["following"] != false {
		t.Error("BT-02: expected following=false after unfollow")
	}
	if rel["blocked"] != false {
		t.Error("BT-02: unfollow must not set blocked=true")
	}
}

func TestFollow_NoAuth_401(t *testing.T) {
	h, _ := newHandler(t)
	w := doRequest(h.Follow, http.MethodPost, "/follows", map[string]any{"followee_id": "acc-bob"}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ─── Relationship response shape ──────────────────────────────────────────────

// TestRelationshipReturnedAfterEveryMutation verifies that every write returns
// the authoritative relationship, so the client never needs to make an extra GET.
func TestRelationshipReturnedAfterEveryMutation(t *testing.T) {
	h, _ := newHandler(t)

	// Follow returns relationship
	w := doRequest(h.Follow, http.MethodPost, "/follows", map[string]any{
		"followee_id": "acc-bob",
	}, "acc-alice")
	var followRes map[string]any
	decode(t, w, &followRes)
	if _, ok := followRes["relationship"]; !ok {
		t.Error("follow response must include 'relationship' key")
	}

	// Block returns relationship
	w2 := doRequest(h.Block, http.MethodPost, "/blocks", map[string]any{
		"blocked_id": "acc-bob",
	}, "acc-alice")
	var blockRes map[string]any
	decode(t, w2, &blockRes)
	if _, ok := blockRes["relationship"]; !ok {
		t.Error("block response must include 'relationship' key")
	}
}

// ─── Privacy ──────────────────────────────────────────────────────────────────

func TestGetPrivacy_DefaultsAreSafe(t *testing.T) {
	h, _ := newHandler(t)
	w := doRequest(h.GetPrivacy, http.MethodGet, "/me/privacy", nil, "acc-alice")

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	var res map[string]any
	decode(t, w, &res)

	// who_can_call defaults to "mutuals" — safe default for a stranger-video product
	if res["WhoCanCall"] != string(profile.PolicyMutuals) &&
		res["who_can_call"] != string(profile.PolicyMutuals) {
		t.Errorf("default who_can_call must be 'mutuals', got %v", res)
	}
}
