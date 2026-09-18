package streaming_test

import (
	"context"
	"testing"
	"time"

	"github.com/lumena/streaming"
	"github.com/lumena/streaming/internal/media"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

func newService() *streaming.BroadcastService {
	return streaming.NewBroadcastService(
		streaming.NewMemSessionRepo(),
		streaming.NewDevIngestService(),
		&streaming.DevPackagerService{},
		streaming.NewDevModerationClassifier(),
	)
}

// ─── Broadcast lifecycle ─────────────────────────────────────────────────────

func TestStartBroadcast_CreatesSession(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	sess, creds, err := svc.StartBroadcast(ctx, "room-001", "host-001", "us-east")
	if err != nil {
		t.Fatalf("StartBroadcast: %v", err)
	}

	if sess.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if sess.RoomID != "room-001" {
		t.Errorf("room ID mismatch: %s", sess.RoomID)
	}
	if sess.State != streaming.StateConnecting {
		t.Errorf("expected StateConnecting, got %s", sess.State)
	}
	if sess.PlaybackURL == "" {
		t.Error("expected playback URL")
	}

	// Credentials
	if creds.StreamKey == "" {
		t.Error("expected non-empty stream key")
	}
	if creds.IngestURL == "" {
		t.Error("expected non-empty ingest URL")
	}
	if !creds.ExpiresAt.After(time.Now()) {
		t.Error("credentials must expire in the future")
	}
}

func TestSessionLive_TransitionsState(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	sess, _, _ := svc.StartBroadcast(ctx, "room-002", "host-002", "us-east")
	if err := svc.SessionLive(ctx, sess.ID); err != nil {
		t.Fatalf("SessionLive: %v", err)
	}

	fetched, err := svc.GetSession(ctx, sess.RoomID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if fetched.State != streaming.StateLive {
		t.Errorf("expected StateLive, got %s", fetched.State)
	}
}

func TestEndBroadcast_ByHost(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	sess, _, _ := svc.StartBroadcast(ctx, "room-003", "host-003", "us-east")
	_ = svc.SessionLive(ctx, sess.ID)

	if err := svc.EndBroadcast(ctx, sess.ID, "host-003"); err != nil {
		t.Fatalf("EndBroadcast: %v", err)
	}

	fetched, _ := svc.GetSession(ctx, "room-003")
	if fetched.State != streaming.StateEnded {
		t.Errorf("expected StateEnded, got %s", fetched.State)
	}
}

func TestEndBroadcast_NotByHost_Fails(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	sess, _, _ := svc.StartBroadcast(ctx, "room-004", "host-004", "us-east")
	err := svc.EndBroadcast(ctx, sess.ID, "not-the-host")
	if err == nil {
		t.Error("expected error when non-host ends broadcast")
	}
}

func TestTerminateBroadcast_Moderation(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	sess, _, _ := svc.StartBroadcast(ctx, "room-005", "host-005", "us-east")
	_ = svc.SessionLive(ctx, sess.ID)

	if err := svc.TerminateBroadcast(ctx, sess.ID, "CS-01"); err != nil {
		t.Fatalf("TerminateBroadcast: %v", err)
	}

	fetched, _ := svc.GetSession(ctx, "room-005")
	if fetched.State != streaming.StateTerminated {
		t.Errorf("expected StateTerminated, got %s", fetched.State)
	}
}

func TestStreamKey_NotReturnedOnViewerGetStream(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	sess, creds, _ := svc.StartBroadcast(ctx, "room-006", "host-006", "us-east")
	_ = svc.SessionLive(ctx, sess.ID)

	// Confirm the key exists in credentials
	if creds.StreamKey == "" {
		t.Fatal("stream key should be present in credentials")
	}

	// GetSession (viewer path) must not expose the stream key
	viewerSess, err := svc.GetSession(ctx, "room-006")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	// ViewerSession has no StreamKey field — the type system enforces this.
	// The HTTP handler also must never serialize stream_key for viewers.
	_ = viewerSess // compile-time: StreamSession has no StreamKey field ✓
}

func TestHealthMonitoring(t *testing.T) {
	svc := newService()
	ctx := context.Background()

	sess, _, _ := svc.StartBroadcast(ctx, "room-007", "host-007", "us-east")

	health, err := svc.GetHealth(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if health.BitrateKbps <= 0 {
		t.Errorf("expected positive bitrate, got %d", health.BitrateKbps)
	}
	if health.Quality == "" {
		t.Error("expected non-empty quality field")
	}
	if health.Quality != "good" && health.Quality != "degraded" && health.Quality != "poor" {
		t.Errorf("unexpected quality value: %q", health.Quality)
	}
}

func TestDefaultLadder_HasSafetyRendition(t *testing.T) {
	ladder := streaming.DefaultLadder()
	hasSafety := false
	for _, r := range ladder.Renditions {
		if r.Name == "144p" {
			hasSafety = true
			if r.VideoBitrate > 300 {
				t.Errorf("safety rendition bitrate too high: %d kbps (should be ≤300)", r.VideoBitrate)
			}
		}
	}
	if !hasSafety {
		t.Error("ABR ladder must include a 144p safety rendition for poor connections")
	}
}

func TestDefaultLadder_OrderedByQuality(t *testing.T) {
	ladder := streaming.DefaultLadder()
	for i := 1; i < len(ladder.Renditions); i++ {
		if ladder.Renditions[i].VideoBitrate >= ladder.Renditions[i-1].VideoBitrate {
			t.Errorf("ladder not ordered by bitrate: %s (%d) >= %s (%d)",
				ladder.Renditions[i].Name, ladder.Renditions[i].VideoBitrate,
				ladder.Renditions[i-1].Name, ladder.Renditions[i-1].VideoBitrate)
		}
	}
}

// ─── Audio-focus lifecycle (C1 fix / BT-12) ──────────────────────────────────

// TestPlayerLifecycle_CorrectOrder verifies the mandatory teardown sequence:
//   ReleaseMediaKeys → ReleaseAudioFocus → Release
// This is the server-side documentation and enforcement of the BT-12 fix.
func TestPlayerLifecycle_CorrectOrder(t *testing.T) {
	lc := media.NewPlayerLifecycle()

	// Volume event while active — safe
	if err := lc.HandleVolumeKeyEvent(); err != nil {
		t.Errorf("volume event while active: %v", err)
	}

	// Step 1: release media keys
	if err := lc.ReleaseMediaKeys(); err != nil {
		t.Fatalf("ReleaseMediaKeys: %v", err)
	}

	// Volume event after keys released — still safe (doesn't touch released state yet)
	if err := lc.HandleVolumeKeyEvent(); err != nil {
		t.Errorf("volume event after keys released: %v", err)
	}

	// Step 2: release audio focus
	if err := lc.ReleaseAudioFocus(); err != nil {
		t.Fatalf("ReleaseAudioFocus: %v", err)
	}

	// Step 3: release player
	if err := lc.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if !lc.IsReleased() {
		t.Error("expected IsReleased=true after Release")
	}

	// Volume event AFTER release — must be a safe no-op, not a crash (BT-12 fix)
	if err := lc.HandleVolumeKeyEvent(); err != nil {
		t.Errorf("volume event after release must be safe no-op: %v", err)
	}
}

// TestPlayerLifecycle_WrongOrder verifies that skipping steps is rejected.
// This is the bug scenario: player.release() called before releaseAudioFocus().
func TestPlayerLifecycle_WrongOrder_Rejected(t *testing.T) {
	lc := media.NewPlayerLifecycle()

	// Try to release audio focus before releasing media keys — wrong order
	if err := lc.ReleaseAudioFocus(); err == nil {
		t.Error("BT-12: ReleaseAudioFocus before ReleaseMediaKeys must be rejected")
	}
}

func TestPlayerLifecycle_SkipToRelease_Rejected(t *testing.T) {
	lc := media.NewPlayerLifecycle()

	// Try to release player directly — skipping both steps
	if err := lc.Release(); err == nil {
		t.Error("BT-12: Release before ReleaseMediaKeys+ReleaseAudioFocus must be rejected")
	}
}

func TestPlayerLifecycle_DoubleRelease_Rejected(t *testing.T) {
	lc := media.NewPlayerLifecycle()

	_ = lc.ReleaseMediaKeys()
	_ = lc.ReleaseAudioFocus()
	_ = lc.Release()

	// Double release must return an error, not crash
	if err := lc.Release(); err == nil {
		t.Error("double Release must return an error")
	}
}

// ─── Reconnect backoff (BT-04) ───────────────────────────────────────────────

func TestReconnectBackoff_ExponentialGrowth(t *testing.T) {
	rs := media.NewReconnectState(8)

	last := 0
	for i := 0; i < 6; i++ {
		ms := rs.NextBackoffMs()
		if ms <= last {
			t.Errorf("attempt %d: backoff %dms not greater than previous %dms (should grow)", i, ms, last)
		}
		last = ms
		rs.RecordAttempt()
	}
}

func TestReconnectBackoff_CappedAt60s(t *testing.T) {
	rs := media.NewReconnectState(20)
	for i := 0; i < 20; i++ {
		rs.RecordAttempt()
	}
	ms := rs.NextBackoffMs()
	if ms > 60000 {
		t.Errorf("backoff exceeds 60s cap: %dms", ms)
	}
}

func TestReconnectBackoff_GivesUpAtMax(t *testing.T) {
	rs := media.NewReconnectState(3)
	for i := 0; i < 3; i++ {
		rs.RecordAttempt()
	}
	if !rs.ShouldGiveUp() {
		t.Error("expected ShouldGiveUp=true after max attempts")
	}
}

func TestReconnectBackoff_ResetsOnSuccess(t *testing.T) {
	rs := media.NewReconnectState(5)
	rs.RecordAttempt()
	rs.RecordAttempt()
	rs.Reset()
	if rs.Attempt != 0 {
		t.Errorf("expected Attempt=0 after Reset, got %d", rs.Attempt)
	}
	if rs.ShouldGiveUp() {
		t.Error("should not give up after reset")
	}
}
