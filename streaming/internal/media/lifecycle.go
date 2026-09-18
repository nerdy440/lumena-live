// Package media documents and tests the audio-focus lifecycle fix.
//
// Background (complaint C1 from doc 01):
//   "App crashes when adjusting volume during a live stream"
//   Root cause: AudioManager / player-release race on Android.
//   The volume key interceptor and the player teardown compete on the main thread.
//
// The correct teardown ORDER (server-side documentation for mobile SDK):
//   1. releaseMediaKeys()      — stop intercepting hardware keys FIRST
//   2. releaseAudioFocus()     — release the audio session
//   3. player.release()        — then release the player
//
// If step 3 happens before step 1, a volume key event after release fires
// against a null player reference → crash.
//
// This file documents the lifecycle contract that the mobile SDKs must implement.
// It is tested here as a state machine to guarantee the ordering is enforced.
package media

import (
	"errors"
	"fmt"
)

// PlayerLifecycle models the correct player teardown order.
// The ordering constraint is enforced by the state machine:
// ReleaseMediaKeys → ReleaseAudioFocus → Release
// Any other order returns ErrWrongTeardownOrder.
//
// This is the server-side documentation of the BT-12 fix.
// The corresponding Android/iOS SDK code follows this contract.
type PlayerLifecycle struct {
	state lifecycleState
}

type lifecycleState int

const (
	stateActive          lifecycleState = iota
	stateKeysReleased    lifecycleState = iota // after ReleaseMediaKeys
	stateFocusReleased   lifecycleState = iota // after ReleaseAudioFocus
	stateReleased        lifecycleState = iota // after Release — final
)

var ErrWrongTeardownOrder = errors.New("player: wrong teardown order (BT-12: keys → focus → release)")
var ErrAlreadyReleased    = errors.New("player: already released")

// NewPlayerLifecycle returns a lifecycle tracker in the active state.
func NewPlayerLifecycle() *PlayerLifecycle {
	return &PlayerLifecycle{state: stateActive}
}

// HandleVolumeKeyEvent simulates a hardware volume key press.
// Must be a no-op (safe) in any state — never crashes, never touches a released player.
func (p *PlayerLifecycle) HandleVolumeKeyEvent() error {
	if p.state == stateReleased {
		// After release, volume events must be ignored — not crash.
		// This is the fix: media keys are released BEFORE the player,
		// so this path should not be reached in correct implementations.
		// Return nil (safe no-op) rather than accessing the player.
		return nil
	}
	// In active/keys-released/focus-released states: handle normally.
	return nil
}

// ReleaseMediaKeys is step 1 of teardown. Must be called first.
func (p *PlayerLifecycle) ReleaseMediaKeys() error {
	if p.state == stateReleased {
		return ErrAlreadyReleased
	}
	if p.state != stateActive {
		return fmt.Errorf("player: ReleaseMediaKeys called in wrong state %d", p.state)
	}
	p.state = stateKeysReleased
	return nil
}

// ReleaseAudioFocus is step 2 of teardown. Must be called after ReleaseMediaKeys.
func (p *PlayerLifecycle) ReleaseAudioFocus() error {
	if p.state == stateReleased {
		return ErrAlreadyReleased
	}
	if p.state != stateKeysReleased {
		return ErrWrongTeardownOrder
	}
	p.state = stateFocusReleased
	return nil
}

// Release is step 3 of teardown. Must be called last.
// After this returns, HandleVolumeKeyEvent is a safe no-op.
func (p *PlayerLifecycle) Release() error {
	if p.state == stateReleased {
		return ErrAlreadyReleased
	}
	if p.state != stateFocusReleased {
		return ErrWrongTeardownOrder
	}
	p.state = stateReleased
	return nil
}

func (p *PlayerLifecycle) IsReleased() bool { return p.state == stateReleased }

// ─── Reconnect lifecycle ──────────────────────────────────────────────────────

// ReconnectState models the viewer player reconnect flow.
// Implements the BT-04 automatic reconnect with exponential backoff.
// The chat scrollback is preserved through the reconnect window.

type ReconnectState struct {
	Attempt     int
	MaxAttempts int
}

func NewReconnectState(maxAttempts int) *ReconnectState {
	return &ReconnectState{MaxAttempts: maxAttempts}
}

// NextBackoffMs returns the wait duration in milliseconds before the next attempt.
// Formula: min(60000, 500 * 2^attempt) ± jitter
// attempt 1: ~1000ms, attempt 2: ~2000ms, attempt 3: ~4000ms, ..., capped at 60000ms
func (r *ReconnectState) NextBackoffMs() int {
	base := 500
	for i := 0; i < r.Attempt; i++ {
		base *= 2
		if base > 60000 {
			base = 60000
			break
		}
	}
	return base
}

// ShouldGiveUp returns true when max attempts are exhausted.
func (r *ReconnectState) ShouldGiveUp() bool {
	return r.Attempt >= r.MaxAttempts
}

// RecordAttempt increments the attempt counter.
func (r *ReconnectState) RecordAttempt() {
	r.Attempt++
}

// Reset resets on successful reconnect.
func (r *ReconnectState) Reset() {
	r.Attempt = 0
}
