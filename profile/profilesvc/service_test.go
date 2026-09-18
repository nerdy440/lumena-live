package profilesvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lumena/profile"
	"github.com/lumena/profile/profilesvc"
	"github.com/lumena/profile/social"
)

func newSvc() *profilesvc.Service {
	profileRepo := profile.NewMemProfileRepo()
	privacyRepo := profile.NewMemPrivacyRepo()
	socialRepo := social.NewMemSocialRepo()
	return profilesvc.NewService(profileRepo, privacyRepo, socialRepo)
}

// ─── Profile CRUD ─────────────────────────────────────────────────────────────

func TestCreateProfile(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	p, err := svc.CreateProfile(ctx, "acc-001", "Alice", "US")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.AccountID != "acc-001" {
		t.Errorf("account ID mismatch")
	}
	if p.DisplayName != "Alice" {
		t.Errorf("display name mismatch")
	}
	if p.Level != 1 {
		t.Errorf("expected level 1, got %d", p.Level)
	}
}

func TestCreateProfile_InvalidDisplayName(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	_, err := svc.CreateProfile(ctx, "acc-001", "", "US")
	if err == nil {
		t.Error("expected error for empty display name")
	}
}

func TestGetProfile_Owner(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	svc.CreateProfile(ctx, "acc-001", "Alice", "US")
	view, err := svc.GetProfile(ctx, "acc-001", "acc-001")
	if err != nil {
		t.Fatalf("get own profile: %v", err)
	}
	if !view.IsOwner {
		t.Error("expected IsOwner=true when viewer == subject")
	}
	if view.Relationship != nil {
		t.Error("expected nil relationship for self-view")
	}
}

func TestGetProfile_Blocked_Unavailable(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	svc.CreateProfile(ctx, "acc-001", "Alice", "US")
	svc.CreateProfile(ctx, "acc-002", "Bob", "US")

	// Bob blocks alice
	svc.Block(ctx, "acc-002", "acc-001")

	// Alice tries to view bob's profile — should get ErrProfileUnavailable
	_, err := svc.GetProfile(ctx, "acc-001", "acc-002")
	if !errors.Is(err, profilesvc.ErrProfileUnavailable) {
		t.Errorf("expected ErrProfileUnavailable when blocked, got: %v", err)
	}

	// Bob viewing alice's profile also unavailable (blocked_by also hides)
	_, err = svc.GetProfile(ctx, "acc-002", "acc-001")
	if !errors.Is(err, profilesvc.ErrProfileUnavailable) {
		t.Errorf("expected ErrProfileUnavailable for blocker viewing blocked, got: %v", err)
	}
}

func TestGetProfile_NotFound(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	_, err := svc.GetProfile(ctx, "", "nonexistent")
	if !errors.Is(err, profilesvc.ErrProfileUnavailable) {
		t.Errorf("expected ErrProfileUnavailable for missing profile, got: %v", err)
	}
}

func TestUpdateProfile_Handle(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "acc-001", "Alice", "US")

	handle := "alice_live"
	p, err := svc.UpdateProfile(ctx, "acc-001", profile.UpdateRequest{Handle: &handle})
	if err != nil {
		t.Fatalf("update handle: %v", err)
	}
	if p.Handle == nil || *p.Handle != handle {
		t.Errorf("expected handle %q, got %v", handle, p.Handle)
	}
}

func TestUpdateProfile_HandleTaken(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "acc-001", "Alice", "US")
	svc.CreateProfile(ctx, "acc-002", "Bob", "US")

	handle := "taken_handle"
	svc.UpdateProfile(ctx, "acc-001", profile.UpdateRequest{Handle: &handle})

	_, err := svc.UpdateProfile(ctx, "acc-002", profile.UpdateRequest{Handle: &handle})
	if !errors.Is(err, profile.ErrHandleTaken) {
		t.Errorf("expected ErrHandleTaken, got: %v", err)
	}
}

func TestUpdateProfile_InvalidHandle(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "acc-001", "Alice", "US")

	cases := []string{"ab", "has spaces", "too-long-for-a-handle-name-that-exceeds-limit", "has@symbol"}
	for _, h := range cases {
		h := h
		_, err := svc.UpdateProfile(ctx, "acc-001", profile.UpdateRequest{Handle: &h})
		if !errors.Is(err, profile.ErrInvalidHandle) {
			t.Errorf("handle %q: expected ErrInvalidHandle, got: %v", h, err)
		}
	}
}

func TestUpdateProfile_AvatarEntersQueue(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "acc-001", "Alice", "US")

	url := "https://example.com/avatar.jpg"
	p, err := svc.UpdateProfile(ctx, "acc-001", profile.UpdateRequest{AvatarURL: &url})
	if err != nil {
		t.Fatalf("update avatar: %v", err)
	}
	// Avatar must be in pending state — not auto-approved
	if p.AvatarState != profile.AvatarPending {
		t.Errorf("expected avatar_state=pending after upload, got %s", p.AvatarState)
	}
}

// ─── Social graph via service (BT-02 / BT-03) ────────────────────────────────

func TestFollow_Unfollow_ViaService(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "alice", "Alice", "US")
	svc.CreateProfile(ctx, "bob", "Bob", "US")

	// Follow
	rel, err := svc.Follow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("follow: %v", err)
	}
	if !rel.Following {
		t.Error("expected following=true")
	}

	// Unfollow — first class, no block needed (BT-02)
	rel, err = svc.Unfollow(ctx, "alice", "bob")
	if err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if rel.Following {
		t.Error("BT-02: expected following=false after direct unfollow")
	}
	if rel.Blocked {
		t.Error("BT-02: unfollow must not create a block")
	}
}

func TestFollow_ImmediateVisibility(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "alice", "Alice", "US")
	svc.CreateProfile(ctx, "bob", "Bob", "US")

	_, _ = svc.Follow(ctx, "alice", "bob")

	// Owner's own following list — must be immediately consistent (BT-03)
	entries, _, err := svc.GetFollowing(ctx, "alice", "alice", "", 50)
	if err != nil {
		t.Fatalf("get following: %v", err)
	}

	found := false
	for _, e := range entries {
		if e.AccountID == "bob" {
			found = true
		}
	}
	if !found {
		t.Error("BT-03: bob must appear in alice's own following list immediately after Follow")
	}
}

// ─── Privacy settings ─────────────────────────────────────────────────────────

func TestPrivacySettings_Defaults(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "acc-001", "Alice", "US")

	settings, err := svc.GetPrivacySettings(ctx, "acc-001")
	if err != nil {
		t.Fatalf("get privacy: %v", err)
	}
	// Safe defaults (doc 10 §7)
	if settings.WhoCanCall != profile.PolicyMutuals {
		t.Errorf("default who_can_call should be mutuals (safe), got %s", settings.WhoCanCall)
	}
	if settings.Matchable {
		t.Error("default matchable should be false (opt-in)")
	}
}

func TestPrivacySettings_Update(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	svc.CreateProfile(ctx, "acc-001", "Alice", "US")

	updated, err := svc.UpdatePrivacySettings(ctx, "acc-001", profile.PrivacySettings{
		WhoCanDM:   profile.PolicyMutuals,
		WhoCanCall: profile.PolicyNobody,
		Matchable:  true,
	})
	if err != nil {
		t.Fatalf("update privacy: %v", err)
	}
	if updated.WhoCanCall != profile.PolicyNobody {
		t.Errorf("expected nobody, got %s", updated.WhoCanCall)
	}
	if !updated.Matchable {
		t.Error("expected matchable=true")
	}
}

// TestCreateProfile_ExistingAccountNeverResetsPrivacy is a regression test:
// GET /users/{id} calls CreateProfile unconditionally on every request as a
// "create a stub if missing" fallback (main.go), which used to also
// unconditionally re-initialize privacy settings back to defaults every
// single call — silently wiping a user's customized who_can_call/
// who_can_dm/matchable settings just from someone (including themselves)
// viewing their own profile a second time. Caught live: a private-video
// call kept failing gate 6 because viewing the callee's profile between
// setting who_can_call=everyone and placing the call reset it back to the
// default "mutuals".
func TestCreateProfile_ExistingAccountNeverResetsPrivacy(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()
	if _, err := svc.CreateProfile(ctx, "acc-001", "Alice", "US"); err != nil {
		t.Fatalf("initial create: %v", err)
	}
	if _, err := svc.UpdatePrivacySettings(ctx, "acc-001", profile.PrivacySettings{
		WhoCanDM: profile.PolicyEveryone, WhoCanCall: profile.PolicyEveryone, Matchable: true,
	}); err != nil {
		t.Fatalf("update privacy: %v", err)
	}

	// Simulate GET /users/{id}'s repeated "auto-create if missing" call —
	// the account already exists, so this must be a no-op for privacy.
	for i := 0; i < 3; i++ {
		if _, err := svc.CreateProfile(ctx, "acc-001", "Alice", "US"); err != nil {
			t.Fatalf("repeat create %d: %v", i, err)
		}
	}

	settings, err := svc.GetPrivacySettings(ctx, "acc-001")
	if err != nil {
		t.Fatalf("get privacy: %v", err)
	}
	if settings.WhoCanCall != profile.PolicyEveryone {
		t.Errorf("expected who_can_call to stay 'everyone' across repeated CreateProfile calls, got %s (privacy was reset)", settings.WhoCanCall)
	}
	if !settings.Matchable {
		t.Error("expected matchable to stay true across repeated CreateProfile calls (privacy was reset)")
	}
}

// TestCreateProfile_DoesNotClobberPrivacySetBeforeProfileExisted is the
// exact scenario that actually bit this in practice: privacy and profile
// are independent records, so nothing stops a client from customizing
// privacy (PATCH /me/privacy, which writes directly to privacyRepo)
// before that account's Profile row has ever been lazily created (GET
// /users/{id}'s auto-create-a-stub fallback in main.go). The first
// CreateProfile call to run after that — for a genuinely brand-new
// profile — must still not stomp the already-customized privacy.
func TestCreateProfile_DoesNotClobberPrivacySetBeforeProfileExisted(t *testing.T) {
	svc := newSvc()
	ctx := context.Background()

	// Privacy customized first — no Profile row exists yet for acc-001.
	if _, err := svc.UpdatePrivacySettings(ctx, "acc-001", profile.PrivacySettings{
		WhoCanDM: profile.PolicyEveryone, WhoCanCall: profile.PolicyEveryone, Matchable: true,
	}); err != nil {
		t.Fatalf("update privacy: %v", err)
	}

	// Now the profile gets lazily created for the first time.
	if _, err := svc.CreateProfile(ctx, "acc-001", "Alice", "US"); err != nil {
		t.Fatalf("create: %v", err)
	}

	settings, err := svc.GetPrivacySettings(ctx, "acc-001")
	if err != nil {
		t.Fatalf("get privacy: %v", err)
	}
	if settings.WhoCanCall != profile.PolicyEveryone {
		t.Errorf("expected who_can_call to stay 'everyone' after the profile's first creation, got %s (privacy was clobbered)", settings.WhoCanCall)
	}
	if !settings.Matchable {
		t.Error("expected matchable to stay true after the profile's first creation (privacy was clobbered)")
	}
}
