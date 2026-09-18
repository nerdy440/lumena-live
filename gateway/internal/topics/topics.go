// Package topics builds and parses the topic strings used across the
// realtime gateway, matching doc 08 §2: room:{id} · user:{id} · sys.
package topics

import "strings"

const Sys = "sys"

func Room(roomID string) string { return "room:" + roomID }
func User(accountID string) string { return "user:" + accountID }
func Conv(conversationID string) string { return "conv:" + conversationID }
func PV(sessionID string) string { return "pv:" + sessionID }

// IsRoom reports whether topic is a room:{id} topic and returns the id.
func IsRoom(topic string) (string, bool) {
	if id, ok := strings.CutPrefix(topic, "room:"); ok {
		return id, true
	}
	return "", false
}

// IsConv reports whether topic is a conv:{id} topic and returns the id.
func IsConv(topic string) (string, bool) {
	if id, ok := strings.CutPrefix(topic, "conv:"); ok {
		return id, true
	}
	return "", false
}

// IsPV reports whether topic is a pv:{id} topic and returns the id.
func IsPV(topic string) (string, bool) {
	if id, ok := strings.CutPrefix(topic, "pv:"); ok {
		return id, true
	}
	return "", false
}

// IsUser reports whether topic is a user:{id} topic and returns the id.
// user:{id} carries an account's private events (BALANCE_CHANGED,
// DIAMONDS_CHANGED, ACCOUNT_STATUS, PV signaling, translated DMs, ...) —
// callers MUST verify the subscribing connection's own account ID matches
// before allowing a SUBSCRIBE, the same way IsConv/IsPV gate their topics.
func IsUser(topic string) (string, bool) {
	if id, ok := strings.CutPrefix(topic, "user:"); ok {
		return id, true
	}
	return "", false
}
