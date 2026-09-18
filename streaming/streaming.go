// Package streaming implements the live streaming domain.
//
// Architecture (doc 09):
//   Broadcaster → WHIP ingest edge → Transcoder → LL-HLS packager → CDN → Viewers
//
// Two separate transport layers:
//   1. 1:many broadcast — LL-HLS/CMAF for scale, ~2s latency
//   2. 1:1 private video — WebRTC P2P (Phase 11)
//
// This phase (5) implements the 1:many broadcast layer.
package streaming

import (
	"context"
	"errors"
	"time"
)

// ─── Stream session states ────────────────────────────────────────────────────

type StreamState string

const (
	StateScheduled    StreamState = "scheduled"
	StateConnecting   StreamState = "connecting"
	StateLive         StreamState = "live"
	StatePaused       StreamState = "paused"
	StateReconnecting StreamState = "reconnecting"
	StateEnded        StreamState = "ended"
	StateTerminated   StreamState = "terminated"
)

// ─── Domain types ─────────────────────────────────────────────────────────────

// StreamSession is an active or completed broadcast session.
type StreamSession struct {
	ID          string        `json:"id"`
	RoomID      string        `json:"room_id"`
	HostID      string        `json:"host_id"`
	State       StreamState   `json:"state"`
	IngestNode  string        `json:"ingest_node"`
	StreamKeyID string        `json:"stream_key_id"` // references the key; key itself never stored
	PlaybackURL string        `json:"playback_url"`  // LL-HLS manifest URL (CDN edge)
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	EndedAt     *time.Time    `json:"ended_at,omitempty"`
	EndReason   string        `json:"end_reason,omitempty"`
	PeakViewers int           `json:"peak_viewers"`
	Health      *StreamHealth `json:"health,omitempty"`
}

// StreamHealth is the real-time broadcast quality snapshot.
// Sent to the broadcaster every 5s as a BROADCAST_HEALTH WebSocket event.
// This is the data behind the "degraded/poor" connection indicator (doc 09 §6).
type StreamHealth struct {
	BitrateKbps   int     `json:"bitrate_kbps"`
	FPS           float64 `json:"fps"`
	DroppedFrames int     `json:"dropped_frames"`
	RTTMs         int     `json:"rtt_ms"`
	Quality       string  `json:"quality"` // good | degraded | poor
	UpdatedAt     time.Time `json:"updated_at"`
}

// IngestCredentials are the short-lived, single-use credentials issued to a broadcaster.
// The stream key is returned ONCE and never stored by the server (only its hash is stored).
// Expires in 5 minutes; single-use.
type IngestCredentials struct {
	SessionID   string    `json:"session_id"`
	IngestURL   string    `json:"ingest_url"`   // wss://ingest-{region}.lumena.live/whip/{token}
	StreamKey   string    `json:"stream_key"`   // NEVER logged; returned once
	ExpiresAt   time.Time `json:"expires_at"`
}

// TranscoderLadder describes the ABR renditions produced from one ingest stream.
// See doc 09 §1 for the full ladder.
type TranscoderLadder struct {
	Renditions []Rendition `json:"renditions"`
}

type Rendition struct {
	Name       string `json:"name"`        // "1080p", "720p", etc.
	VideoBitrate int  `json:"video_kbps"`
	AudioBitrate int  `json:"audio_kbps"`
	Codec      string `json:"codec"`       // "h264"
}

// DefaultLadder is the production ABR ladder (doc 09 table).
func DefaultLadder() TranscoderLadder {
	return TranscoderLadder{Renditions: []Rendition{
		{Name: "1080p", VideoBitrate: 4500, AudioBitrate: 192, Codec: "h264"},
		{Name: "720p",  VideoBitrate: 2500, AudioBitrate: 128, Codec: "h264"},
		{Name: "480p",  VideoBitrate: 1200, AudioBitrate: 96,  Codec: "h264"},
		{Name: "360p",  VideoBitrate: 600,  AudioBitrate: 96,  Codec: "h264"},
		{Name: "144p",  VideoBitrate: 200,  AudioBitrate: 64,  Codec: "h264"}, // safety/low-bandwidth
	}}
}

// ModerationFrame is one frame sample sent to the moderation classifier.
type ModerationFrame struct {
	SessionID  string
	Timestamp  time.Time
	FrameData  []byte // raw JPEG; never persisted to DB; only forwarded to classifier
}

// ModerationResult is the classifier response for a frame.
type ModerationResult struct {
	SessionID  string
	Timestamp  time.Time
	Classes    []ClassLabel
	Action     ModerationAction
}

type ClassLabel struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
}

type ModerationAction string

const (
	ActionNone      ModerationAction = "none"
	ActionWarn      ModerationAction = "warn"
	ActionTerminate ModerationAction = "terminate" // immediate stream termination
)

// ─── Errors ───────────────────────────────────────────────────────────────────

var (
	ErrSessionNotFound   = errors.New("streaming: session not found")
	ErrSessionNotLive    = errors.New("streaming: session is not live")
	ErrAgeNotAssured     = errors.New("streaming: broadcaster age not assured")
	ErrAccountRestricted = errors.New("streaming: broadcaster account restricted")
	ErrIngestUnreachable = errors.New("streaming: ingest node unreachable")
)

// ─── Repository ───────────────────────────────────────────────────────────────

// SessionRepo persists stream sessions.
type SessionRepo interface {
	Create(ctx context.Context, s StreamSession) (*StreamSession, error)
	Get(ctx context.Context, sessionID string) (*StreamSession, error)
	GetByRoom(ctx context.Context, roomID string) (*StreamSession, error)
	GetActive(ctx context.Context) ([]StreamSession, error)
	UpdateState(ctx context.Context, sessionID string, state StreamState, reason string) error
	UpdateHealth(ctx context.Context, sessionID string, h StreamHealth) error
	End(ctx context.Context, sessionID string, reason string) error
	// UpdatePlaybackURL persists the CDN playback URL after session creation.
	UpdatePlaybackURL(ctx context.Context, sessionID, url string) error
}

// ─── Services ─────────────────────────────────────────────────────────────────

// IngestService handles the broadcaster connection lifecycle.
// In production this talks to a WHIP-capable SFU (Janus, mediasoup, etc.).
// Phase 5 ships the full API contract; the SFU vendor is pluggable.
type IngestService interface {
	// IssueCredentials creates a short-lived WHIP ingest URL + stream key.
	// The stream key hash is stored; the raw key is returned once and never stored.
	IssueCredentials(ctx context.Context, sessionID, region string) (*IngestCredentials, error)
	// GetHealth returns current stream health metrics for a session.
	GetHealth(ctx context.Context, sessionID string) (*StreamHealth, error)
	// Terminate forces an ingest session to end (used by moderation).
	Terminate(ctx context.Context, sessionID string) error
}

// PackagerService manages the LL-HLS manifest for viewers.
type PackagerService interface {
	// PlaybackURL returns the viewer-facing LL-HLS manifest URL.
	PlaybackURL(sessionID string) string
	// IsReady returns true when at least one complete HLS segment is available.
	IsReady(ctx context.Context, sessionID string) bool
}

// ModerationClassifier submits frame samples and returns actions.
type ModerationClassifier interface {
	// ClassifyFrame sends one frame to the classifier.
	// Non-blocking: result delivered via ResultChan.
	ClassifyFrame(frame ModerationFrame)
	// ResultChan returns the channel on which classification results are delivered.
	ResultChan() <-chan ModerationResult
}
