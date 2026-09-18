// Package streaming — broadcast application service.
//
// Implements the full broadcast lifecycle:
//   Start → credentials → LIVE → health monitoring → frame moderation → End/Terminate
//
// The audio-focus lifecycle fix (BT-12 / complaint C1) is documented in
// the player lifecycle section below. The server-side fix here is the
// correct health reporting that lets the client know when to degrade gracefully.
package streaming

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// AutoTerminationHook is notified after an automated classifier match ends a
// broadcast, so the moderation module (Phase 15) can record a real
// enforcement (rule_id, evidence_ref, decided_by: "auto:...") instead of the
// termination existing only as a session-state transition. Optional — a nil
// hook just skips the notification, same as every other optional Phase-15
// wiring in this codebase.
type AutoTerminationHook func(ctx context.Context, hostID, sessionID, ruleID string)

// BroadcastService orchestrates the full streaming lifecycle.
type BroadcastService struct {
	sessions    SessionRepo
	ingest      IngestService
	packager    PackagerService
	classifier  ModerationClassifier
	onAutoTerm  AutoTerminationHook

	// healthSubs allows the health monitor goroutine to push updates.
	mu          sync.RWMutex
	healthSubs  map[string]chan StreamHealth // sessionID → subscriber channel
}

// WithAutoTerminationHook registers the moderation module's enforcement
// recorder. Must be called before any broadcast starts (composition root
// wiring time), same convention as gateway/ws's With* builders.
func (s *BroadcastService) WithAutoTerminationHook(hook AutoTerminationHook) *BroadcastService {
	s.onAutoTerm = hook
	return s
}

func NewBroadcastService(
	sessions SessionRepo,
	ingest IngestService,
	packager PackagerService,
	classifier ModerationClassifier,
) *BroadcastService {
	svc := &BroadcastService{
		sessions:   sessions,
		ingest:     ingest,
		packager:   packager,
		classifier: classifier,
		healthSubs: make(map[string]chan StreamHealth),
	}
	// Start the moderation result consumer.
	go svc.handleModerationResults()
	return svc
}

// ─── Broadcaster lifecycle ────────────────────────────────────────────────────

// StartBroadcast creates a stream session and issues ingest credentials.
// Gate checks (age assurance, T&S standing) must be verified by the caller
// before invoking this — this service trusts that the gate has already passed.
func (s *BroadcastService) StartBroadcast(ctx context.Context, roomID, hostID, region string) (*StreamSession, *IngestCredentials, error) {
	// Create the session record.
	sess, err := s.sessions.Create(ctx, StreamSession{
		RoomID:  roomID,
		HostID:  hostID,
		State:   StateConnecting,
		IngestNode: region,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("streaming: create session: %w", err)
	}

	// Issue short-lived single-use ingest credentials.
	// The stream key is generated here and returned once — never stored.
	creds, err := s.ingest.IssueCredentials(ctx, sess.ID, region)
	if err != nil {
		_ = s.sessions.End(ctx, sess.ID, "credential_issue_failed")
		return nil, nil, fmt.Errorf("streaming: issue credentials: %w", err)
	}

	// Compute and persist the CDN playback URL (requires session ID, done after Create).
	playbackURL := s.packager.PlaybackURL(sess.ID)
	sess.PlaybackURL = playbackURL
	_ = s.sessions.UpdatePlaybackURL(ctx, sess.ID, playbackURL)

	// Start background health monitor for this session.
	go s.monitorHealth(sess.ID)

	return sess, creds, nil
}

// SessionLive marks a session as live — called when the ingest node
// confirms the first valid media packet has arrived.
func (s *BroadcastService) SessionLive(ctx context.Context, sessionID string) error {
	return s.sessions.UpdateState(ctx, sessionID, StateLive, "")
}

// EndBroadcast ends a stream gracefully (host action).
func (s *BroadcastService) EndBroadcast(ctx context.Context, sessionID, hostID string) error {
	sess, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return ErrSessionNotFound
	}
	if sess.HostID != hostID {
		return fmt.Errorf("streaming: only the host can end a broadcast")
	}

	if err := s.ingest.Terminate(ctx, sessionID); err != nil {
		// Log but proceed — the session record must be updated regardless.
		fmt.Printf("streaming: terminate ingest (non-fatal): %v\n", err)
	}

	s.cleanupHealthSub(sessionID)
	return s.sessions.End(ctx, sessionID, "host_ended")
}

// TerminateBroadcast ends a stream immediately for moderation reasons.
// Called by the moderation pipeline when the classifier returns ActionTerminate.
// The ruleID identifies the policy that was violated — mandatory for BT-09.
func (s *BroadcastService) TerminateBroadcast(ctx context.Context, sessionID, ruleID string) error {
	_ = s.ingest.Terminate(ctx, sessionID)
	s.cleanupHealthSub(sessionID)

	// The STREAM_ENDED event with rule_id is emitted by the WebSocket layer,
	// not here — this service is transport-agnostic.
	return s.sessions.UpdateState(ctx, sessionID, StateTerminated,
		fmt.Sprintf("moderation:%s", ruleID))
}

// GetSession returns session details for the room-join flow.
func (s *BroadcastService) GetSession(ctx context.Context, roomID string) (*StreamSession, error) {
	return s.sessions.GetByRoom(ctx, roomID)
}

// GetHealth returns the current health metrics for a broadcaster.
func (s *BroadcastService) GetHealth(ctx context.Context, sessionID string) (*StreamHealth, error) {
	return s.ingest.GetHealth(ctx, sessionID)
}

// SubscribeHealth returns a channel that receives health updates every 5s.
// The caller must call UnsubscribeHealth when done.
func (s *BroadcastService) SubscribeHealth(sessionID string) <-chan StreamHealth {
	ch := make(chan StreamHealth, 10)
	s.mu.Lock()
	s.healthSubs[sessionID] = ch
	s.mu.Unlock()
	return ch
}

func (s *BroadcastService) cleanupHealthSub(sessionID string) {
	s.mu.Lock()
	if ch, ok := s.healthSubs[sessionID]; ok {
		close(ch)
		delete(s.healthSubs, sessionID)
	}
	s.mu.Unlock()
}

// ─── Health monitoring goroutine ─────────────────────────────────────────────
//
// Runs for the lifetime of each active session.
// Polls the ingest node every 5s and broadcasts health to subscribers.
// This drives the broadcaster's connection quality indicator (doc 09 §6):
//   good → green | degraded → amber | poor → red | reconnecting → spinner
//
// Also triggers frame sampling for the moderation classifier (doc 09 §4):
// every 10s a frame sample is submitted to the classifier.

func (s *BroadcastService) monitorHealth(sessionID string) {
	ticker := time.NewTicker(5 * time.Second)
	frameTicker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	defer frameTicker.Stop()

	ctx := context.Background()

	for {
		select {
		case <-ticker.C:
			health, err := s.ingest.GetHealth(ctx, sessionID)
			if err != nil {
				// Session ended — stop monitoring.
				return
			}

			// Persist latest health to the session record.
			_ = s.sessions.UpdateHealth(ctx, sessionID, *health)

			// Fan out to subscribers (broadcaster's WebSocket connection).
			s.mu.RLock()
			ch, ok := s.healthSubs[sessionID]
			s.mu.RUnlock()
			if !ok {
				return // no subscribers = session is over
			}
			select {
			case ch <- *health:
			default:
				// Subscriber's buffer is full — drop this update; next will arrive in 5s.
			}

		case <-frameTicker.C:
			// Submit a frame sample to the moderation classifier (doc 09 §4).
			// In production: the ingest node captures a JPEG frame and sends it here.
			// DEV MOCK: sends an empty frame — classifier will return ActionNone.
			sess, err := s.sessions.Get(ctx, sessionID)
			if err != nil || sess.State == StateEnded || sess.State == StateTerminated {
				return
			}
			s.classifier.ClassifyFrame(ModerationFrame{
				SessionID: sessionID,
				Timestamp: time.Now(),
				FrameData: []byte{}, // DEV MOCK — real: JPEG bytes from ingest node
			})
		}
	}
}

// handleModerationResults consumes the classifier's result channel.
// On ActionTerminate: immediately ends the session (no human review before stopping).
// Human review follows AFTER termination; the reviewer can reinstate within 15 minutes.
// This is the one place where automated action precedes human review (doc 09 §4).
func (s *BroadcastService) handleModerationResults() {
	for result := range s.classifier.ResultChan() {
		if result.Action == ActionTerminate {
			ctx := context.Background()
			// The rule ID for auto-terminations — CS-01 (CSAM) is the primary trigger.
			// Real: classifier returns the specific rule ID.
			ruleID := "CS-01"
			for _, c := range result.Classes {
				if c.Label != "safe" && c.Confidence > 0.9 {
					ruleID = classToRuleID(c.Label)
				}
			}
			hostID := ""
			if sess, err := s.sessions.Get(ctx, result.SessionID); err == nil {
				hostID = sess.HostID
			}
			_ = s.TerminateBroadcast(ctx, result.SessionID, ruleID)
			if s.onAutoTerm != nil && hostID != "" {
				s.onAutoTerm(ctx, hostID, result.SessionID, ruleID)
			}
		}
	}
}

func classToRuleID(label string) string {
	switch label {
	case "csam":
		return "CS-01"
	case "explicit":
		return "SX-01"
	case "violence":
		return "VL-01"
	default:
		return "CS-01"
	}
}
