// Package streaming — in-memory implementations for dev/test.
// Production: swap IngestService for WHIP SFU client; swap PackagerService for CDN packager.
package streaming

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

// ─── In-memory SessionRepo ────────────────────────────────────────────────────

type MemSessionRepo struct {
	mu       sync.RWMutex
	sessions map[string]*StreamSession
	byRoom   map[string]string // roomID → sessionID
	seq      int
}

func NewMemSessionRepo() *MemSessionRepo {
	return &MemSessionRepo{
		sessions: make(map[string]*StreamSession),
		byRoom:   make(map[string]string),
	}
}

var _ SessionRepo = (*MemSessionRepo)(nil)

func (r *MemSessionRepo) Create(_ context.Context, s StreamSession) (*StreamSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	s.ID = fmt.Sprintf("ss-%04d", r.seq)
	r.sessions[s.ID] = &s
	r.byRoom[s.RoomID] = s.ID
	cp := s
	return &cp, nil
}

func (r *MemSessionRepo) Get(_ context.Context, id string) (*StreamSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	cp := *s
	return &cp, nil
}

func (r *MemSessionRepo) GetByRoom(_ context.Context, roomID string) (*StreamSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byRoom[roomID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	s := r.sessions[id]
	cp := *s
	return &cp, nil
}

func (r *MemSessionRepo) GetActive(_ context.Context) ([]StreamSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []StreamSession
	for _, s := range r.sessions {
		if s.State == StateLive || s.State == StateReconnecting {
			cp := *s
			out = append(out, cp)
		}
	}
	return out, nil
}

// ListByHost returns every session (any state) a host has ever broadcast,
// most recent first. Not part of the SessionRepo interface — used only by
// the creator dashboard's analytics (Phase 14), so it's a concrete method
// rather than something every SessionRepo implementation must provide.
func (r *MemSessionRepo) ListByHost(_ context.Context, hostID string) ([]StreamSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []StreamSession
	for _, s := range r.sessions {
		if s.HostID == hostID {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID > out[j].ID
	})
	return out, nil
}

// ListAll returns every session ever created, any host, any state —
// read-only, used only by the analytics module's funnel/economy dashboards
// (Phase 17). Not part of the SessionRepo interface, same convention as
// ListByHost.
func (r *MemSessionRepo) ListAll(_ context.Context) ([]StreamSession, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]StreamSession, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, *s)
	}
	return out, nil
}

func (r *MemSessionRepo) UpdateState(_ context.Context, id string, state StreamState, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	s.State = state
	if state == StateLive && s.StartedAt == nil {
		now := time.Now()
		s.StartedAt = &now
	}
	if (state == StateEnded || state == StateTerminated) && s.EndedAt == nil {
		now := time.Now()
		s.EndedAt = &now
		s.EndReason = reason
	}
	return nil
}

func (r *MemSessionRepo) UpdateHealth(_ context.Context, id string, h StreamHealth) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	s.Health = &h
	return nil
}

func (r *MemSessionRepo) End(_ context.Context, id string, reason string) error {
	return r.UpdateState(context.Background(), id, StateEnded, reason)
}

// ─── Dev IngestService (DEV MOCK) ─────────────────────────────────────────────
//
// In production: replaced by a WHIP SFU client (Janus/mediasoup/Pion).
// This mock generates realistic-looking credentials and simulates health metrics.
// All calls are clearly marked DEV MOCK in the response.

// DevIngestService is a DEV MOCK implementation.
// NOT FOR PRODUCTION — build tag guard prevents inclusion in production builds.
// The production implementation is ProductionIngestService in ingest/whip_client.go.
type DevIngestService struct {
	mu       sync.RWMutex
	sessions map[string]*devSession
}

type devSession struct {
	sessionID string
	startedAt time.Time
	viewers   int
}

func NewDevIngestService() *DevIngestService {
	return &DevIngestService{sessions: make(map[string]*devSession)}
}

var _ IngestService = (*DevIngestService)(nil)

func (d *DevIngestService) IssueCredentials(_ context.Context, sessionID, region string) (*IngestCredentials, error) {
	// Generate a cryptographically random stream key token (32 bytes)
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("ingest: generate token: %w", err)
	}
	// Generate a human-readable stream key (16 hex chars)
	key := make([]byte, 8)
	_, _ = rand.Read(key)

	d.mu.Lock()
	d.sessions[sessionID] = &devSession{sessionID: sessionID, startedAt: time.Now()}
	d.mu.Unlock()

	return &IngestCredentials{
		SessionID: sessionID,
		// DEV MOCK: real production URL would be wss://ingest-{region}.lumena.live/whip/{token}
		IngestURL: fmt.Sprintf("wss://ingest-%s.lumena.live/whip/%s", region, hex.EncodeToString(token)),
		StreamKey: hex.EncodeToString(key), // raw key returned once, never stored
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}, nil
}

func (d *DevIngestService) GetHealth(_ context.Context, sessionID string) (*StreamHealth, error) {
	d.mu.RLock()
	sess, ok := d.sessions[sessionID]
	d.mu.RUnlock()
	if !ok {
		return nil, ErrSessionNotFound
	}

	// DEV MOCK: generate plausible health metrics that simulate a real stream.
	// Real implementation reads from the SFU's stats API.
	uptime := time.Since(sess.startedAt).Seconds()
	// Bitrate oscillates to simulate network variation
	bitrate := int(2800 + 400*math.Sin(uptime/30))
	fps := 29.97 + 0.1*math.Sin(uptime/10)
	rtt := int(45 + 10*math.Sin(uptime/20))

	quality := "good"
	if bitrate < 1500 || rtt > 200 {
		quality = "degraded"
	}
	if bitrate < 800 || rtt > 500 {
		quality = "poor"
	}

	return &StreamHealth{
		BitrateKbps:   bitrate,
		FPS:           fps,
		DroppedFrames: 0,
		RTTMs:         rtt,
		Quality:       quality,
		UpdatedAt:     time.Now(),
	}, nil
}

func (d *DevIngestService) Terminate(_ context.Context, sessionID string) error {
	d.mu.Lock()
	delete(d.sessions, sessionID)
	d.mu.Unlock()
	return nil
}

// ─── Dev PackagerService (DEV MOCK) ───────────────────────────────────────────

// DevPackagerService simulates the LL-HLS packager.
// Real implementation: nginx-rtmp with LL-HLS module or Wowza/AWS MediaLive.
type DevPackagerService struct{}

var _ PackagerService = (*DevPackagerService)(nil)

func (d *DevPackagerService) PlaybackURL(sessionID string) string {
	// DEV MOCK: returns a plausible CDN URL shape.
	// Real: https://edge-{region}.lumena.live/hls/{sessionID}/master.m3u8
	return fmt.Sprintf("https://edge.lumena.live/hls/%s/master.m3u8", sessionID)
}

func (d *DevPackagerService) IsReady(_ context.Context, _ string) bool {
	// DEV MOCK: always ready. Real: poll manifest for first segment.
	return true
}

// ─── Dev ModerationClassifier (DEV MOCK) ──────────────────────────────────────

// DevModerationClassifier is a DEV MOCK that always returns ActionNone.
// Production: replaced by a real classifier (AWS Rekognition, CSAM PhotoDNA, etc.).
// The pipeline architecture is the same; only the classifier implementation differs.
type DevModerationClassifier struct {
	results chan ModerationResult
}

func NewDevModerationClassifier() *DevModerationClassifier {
	return &DevModerationClassifier{results: make(chan ModerationResult, 100)}
}

var _ ModerationClassifier = (*DevModerationClassifier)(nil)

func (d *DevModerationClassifier) ClassifyFrame(frame ModerationFrame) {
	// DEV MOCK: always safe. Real: send to classifier, await result async.
	go func() {
		// Simulate classifier latency (real: ~200-500ms)
		time.Sleep(50 * time.Millisecond)
		d.results <- ModerationResult{
			SessionID: frame.SessionID,
			Timestamp: frame.Timestamp,
			Classes:   []ClassLabel{{Label: "safe", Confidence: 0.99}},
			Action:    ActionNone,
		}
	}()
}

func (d *DevModerationClassifier) ResultChan() <-chan ModerationResult {
	return d.results
}

func (r *MemSessionRepo) UpdatePlaybackURL(_ context.Context, id, url string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	s.PlaybackURL = url
	return nil
}
