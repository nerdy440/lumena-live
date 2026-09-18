package main

// Local-only persistence: a lightweight stand-in for a real database so a
// personally-hosted instance's accounts, wallets, and social graph survive
// a server restart, without taking on the scope of standing up Postgres
// (migrations, connection pooling, a real schema). This snapshots five of
// the highest-value repos — the rest (rooms, chat, moderation, etc.) stay
// in-memory-only, a deliberate scope cut, not an oversight: losing an
// in-progress live room or a chat history on restart is a shrug for a
// single-user local instance, losing your account or your coin balance
// is not. NOT a substitute for a real database in any deployment serving
// other people's money or data — see README's production-readiness notes.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"
)

// snapshotter is implemented by every repo this file persists.
type snapshotter interface {
	Snapshot() ([]byte, error)
}

// restorer is implemented by every repo this file restores into.
type restorer interface {
	Restore([]byte) error
}

type localState struct {
	Auth    json.RawMessage `json:"auth,omitempty"`
	Profile json.RawMessage `json:"profile,omitempty"`
	Privacy json.RawMessage `json:"privacy,omitempty"`
	Social  json.RawMessage `json:"social,omitempty"`
	Ledger  json.RawMessage `json:"ledger,omitempty"`
}

// localStateRepos bundles the five repos loadLocalState/saveLocalState
// operate on, named to match localState's fields.
type localStateRepos struct {
	Auth    restorerSnapshotter
	Profile restorerSnapshotter
	Privacy restorerSnapshotter
	Social  restorerSnapshotter
	Ledger  restorerSnapshotter
}

type restorerSnapshotter interface {
	snapshotter
	restorer
}

// loadLocalState reads statePath (if present — a fresh install has none)
// and restores each repo's state. Called once at startup, before the
// server accepts any traffic, matching Snapshot/Restore's own
// "only meaningful before concurrent writes" contract.
func loadLocalState(logger *slog.Logger, statePath string, repos localStateRepos) {
	data, err := os.ReadFile(statePath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn("local state: could not read state file, starting fresh", "error", err)
		}
		return
	}
	var s localState
	if err := json.Unmarshal(data, &s); err != nil {
		logger.Warn("local state: could not parse state file, starting fresh", "error", err)
		return
	}
	restore := func(name string, r restorerSnapshotter, raw json.RawMessage) {
		if len(raw) == 0 {
			return
		}
		if err := r.Restore(raw); err != nil {
			logger.Warn("local state: could not restore, starting that part fresh", "part", name, "error", err)
		}
	}
	restore("auth", repos.Auth, s.Auth)
	restore("profile", repos.Profile, s.Profile)
	restore("privacy", repos.Privacy, s.Privacy)
	restore("social", repos.Social, s.Social)
	restore("ledger", repos.Ledger, s.Ledger)
	logger.Info("local state: restored from disk", "path", statePath)
}

// saveLocalState snapshots every repo and writes them atomically (write to
// a temp file, then rename — avoids a half-written state file if the
// process is killed mid-write).
func saveLocalState(logger *slog.Logger, statePath string, repos localStateRepos) {
	snap := func(name string, r restorerSnapshotter) json.RawMessage {
		b, err := r.Snapshot()
		if err != nil {
			logger.Warn("local state: could not snapshot, skipping", "part", name, "error", err)
			return nil
		}
		return b
	}
	s := localState{
		Auth:    snap("auth", repos.Auth),
		Profile: snap("profile", repos.Profile),
		Privacy: snap("privacy", repos.Privacy),
		Social:  snap("social", repos.Social),
		Ledger:  snap("ledger", repos.Ledger),
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		logger.Error("local state: could not marshal state", "error", err)
		return
	}
	tmpPath := statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		logger.Error("local state: could not write state file", "error", err)
		return
	}
	if err := os.Rename(tmpPath, statePath); err != nil {
		logger.Error("local state: could not finalize state file", "error", err)
	}
}

// startPeriodicSave saves every interval until ctx is cancelled — the
// safety net between restarts if the process is ever killed without a
// clean shutdown (power loss, task-kill, a crash).
func startPeriodicSave(ctx context.Context, logger *slog.Logger, statePath string, repos localStateRepos, interval time.Duration) {
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				saveLocalState(logger, statePath, repos)
			}
		}
	}()
}
