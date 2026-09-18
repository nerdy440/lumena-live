package ws_test

// Verifies the viewer-count fix (feed/gateway bug found live: viewer_count
// stayed frozen at 0 because nothing ever tracked it) and its follow-up
// refinement — the room's own host is never counted as one of their own
// viewers, matching the reference product.

import (
	"context"
	"testing"

	"github.com/lumena/gateway/bus"
	"github.com/lumena/gateway/ws"
)

func isHostOnly(hostID string) ws.IsHostFunc {
	return func(_ context.Context, _, accountID string) (bool, error) {
		return accountID == hostID, nil
	}
}

func viewerCountOf(t *testing.T, ev bus.Event) int {
	t.Helper()
	payload, ok := ev.Payload.(map[string]any)
	if !ok {
		t.Fatalf("expected map payload on %s, got %T", ev.Type, ev.Payload)
	}
	n, ok := payload["viewer_count"].(float64) // JSON numbers decode as float64
	if !ok {
		t.Fatalf("expected numeric viewer_count in %s payload, got %+v", ev.Type, payload)
	}
	return int(n)
}

func TestWS_ViewerCount_TracksRealSubscribersNotFrozenAtZero(t *testing.T) {
	b := bus.New()
	tokens := ws.NewTokenStore()
	srv := newTestServer(t, b, tokens, isHostOnly("host1"))

	hostTok := tokens.Issue("host1", "device-h")
	hostConn := dial(t, srv, hostTok)
	mustWrite(t, hostConn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, hostConn) // SUBSCRIBED
	hostJoined := readEvent(t, hostConn)
	if got := viewerCountOf(t, hostJoined); got != 0 {
		t.Fatalf("expected viewer_count 0 when only the host has subscribed (host excluded), got %d", got)
	}

	viewerTok := tokens.Issue("alice", "device-a")
	viewerConn := dial(t, srv, viewerTok)
	mustWrite(t, viewerConn, map[string]any{"type": "SUBSCRIBE", "topic": "room:1"})
	_ = readEvent(t, viewerConn) // SUBSCRIBED
	viewerJoined := readEvent(t, viewerConn)
	if got := viewerCountOf(t, viewerJoined); got != 1 {
		t.Fatalf("expected viewer_count 1 after a real viewer joins, got %d", got)
	}

	// The host, still connected and subscribed, must see the same live update.
	hostSeesJoin := readEvent(t, hostConn)
	if hostSeesJoin.Type != "VIEWER_JOINED" {
		t.Fatalf("expected host to observe the viewer's VIEWER_JOINED, got %+v", hostSeesJoin)
	}
	if got := viewerCountOf(t, hostSeesJoin); got != 1 {
		t.Fatalf("expected host's view of viewer_count to also read 1, got %d", got)
	}

	mustWrite(t, viewerConn, map[string]any{"type": "UNSUBSCRIBE", "topic": "room:1"})
	viewerLeft := readEvent(t, hostConn) // host observes the departure
	if viewerLeft.Type != "VIEWER_LEFT" {
		t.Fatalf("expected VIEWER_LEFT, got %+v", viewerLeft)
	}
	if got := viewerCountOf(t, viewerLeft); got != 0 {
		t.Fatalf("expected viewer_count back to 0 after the only real viewer leaves, got %d", got)
	}
}
