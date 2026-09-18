// Package feed — recommendation engine implementation.
// See doc 05 §L and doc 12 §L for design rationale.
//
// Key principle: NOT just sorted by viewer count (that produces rich-get-richer).
// Score = weighted sum of freshness + engagement + viewer_growth + gift_activity
//         + language_match + region_relevance + follow_boost + recently_viewed_penalty.
package feed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"sort"
	"time"
)

// ─── Engine interface ─────────────────────────────────────────────────────────

// Engine ranks room candidates for a user context.
// RuleBasedEngine is the Phase 4 MVP.
// MLEngine is the Phase 17 upgrade — same interface, drop-in replacement.
type Engine interface {
	Rank(ctx context.Context, rooms []Room, user UserFeedContext) []Room
	Name() string
}

// ─── RuleBasedEngine ──────────────────────────────────────────────────────────

// Weights controls the contribution of each scoring signal.
// Exposed so they can be tuned via remote config without a deploy.
type Weights struct {
	Freshness         float64
	Engagement        float64 // log-normalized viewer count
	ViewerGrowth      float64 // momentum signal
	GiftActivity      float64 // economic engagement
	LanguageMatch     float64
	RegionRelevance   float64
	FollowBoost       float64 // user follows this host
	RecentlyViewedPen float64 // penalty multiplier (0–1; applied multiplicatively)
}

// DefaultWeights returns the production starting point.
// Each weight is tunable; they do not need to sum to 1.
func DefaultWeights() Weights {
	return Weights{
		Freshness:         0.15,
		Engagement:        0.20,
		ViewerGrowth:      0.12,
		GiftActivity:      0.15,
		LanguageMatch:     0.18,
		RegionRelevance:   0.10,
		FollowBoost:       0.15,
		RecentlyViewedPen: 0.60,
	}
}

// RuleBasedEngine is the Phase 4 recommendation engine.
type RuleBasedEngine struct {
	weights Weights
}

func NewRuleBasedEngine(w Weights) *RuleBasedEngine {
	return &RuleBasedEngine{weights: w}
}

func (e *RuleBasedEngine) Name() string { return "rule-based-v1" }

func (e *RuleBasedEngine) Rank(_ context.Context, rooms []Room, user UserFeedContext) []Room {
	w := e.weights
	langSet := toSet(user.Languages)
	followSet := toSet(user.FollowedHostIDs)
	viewedSet := toSet(user.RecentlyViewedIDs)

	type scored struct {
		room  Room
		score float64
	}
	candidates := make([]scored, 0, len(rooms))

	for _, room := range rooms {
		if room.Status != RoomLive {
			continue // Hot and ForYou only show live rooms
		}
		s := 0.0

		// Freshness: exponential decay with ~60min half-life.
		// A stream started 2 hours ago gets ~25% of the freshness score.
		if room.StartedAt != nil {
			ageMin := time.Since(*room.StartedAt).Minutes()
			s += w.Freshness * math.Exp(-0.012*ageMin)
		}

		// Engagement: log-normalised viewer count.
		// log(1+viewers)/log(1+10000) caps at ~1.0 for very large rooms.
		// This prevents 10k-viewer rooms from crushing all smaller ones.
		engagement := math.Log1p(float64(room.ViewerCount)) / math.Log1p(10000)
		s += w.Engagement * clamp(engagement, 0, 1)

		// Viewer growth rate: momentum signal.
		// A room growing fast is more interesting than a stagnant large room.
		s += w.ViewerGrowth * clamp(room.FollowerGrowthRate/5.0, 0, 1)

		// Gift activity: proxy for creator energy and room quality.
		s += w.GiftActivity * clamp(room.GiftVelocity/8.0, 0, 1)

		// Language match: full score for a match, 20% for cross-language discovery.
		if langSet[room.Language] {
			s += w.LanguageMatch
		} else {
			s += w.LanguageMatch * 0.20
		}

		// Region relevance: same region = full score.
		if room.RegionCode == user.RegionCode {
			s += w.RegionRelevance
		} else {
			s += w.RegionRelevance * 0.25
		}

		// Follow boost: rooms by followed hosts rank significantly higher.
		if followSet[room.HostID] {
			s += w.FollowBoost
		}

		// Recently-viewed penalty: reduce score if already seen today.
		if viewedSet[room.RoomID] {
			s *= (1.0 - w.RecentlyViewedPen)
		}

		candidates = append(candidates, scored{room, s})
	}

	// Stable sort: ties broken by viewer count descending.
	sort.SliceStable(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].score-candidates[j].score) < 0.001 {
			return candidates[i].room.ViewerCount > candidates[j].room.ViewerCount
		}
		return candidates[i].score > candidates[j].score
	})

	result := make([]Room, len(candidates))
	for i, c := range candidates {
		result[i] = c.room
	}
	return result
}

// ─── MLEngine placeholder ─────────────────────────────────────────────────────

// MLEngine satisfies the Engine interface.
// NOT IMPLEMENTED — Phase 17. The interface is designed now so the swap
// from rule-based to ML requires zero changes in calling code.
type MLEngine struct{}

func (m *MLEngine) Name() string { return "ml-v1" }
func (m *MLEngine) Rank(_ context.Context, rooms []Room, _ UserFeedContext) []Room {
	// NOT IMPLEMENTED — Phase 17.
	// Will call a model-serving endpoint with feature vectors.
	// Falls back to RuleBasedEngine if the model is unavailable.
	return rooms
}

// ─── Cursor-stable pagination (BT-05) ─────────────────────────────────────────
//
// Problem: a naive offset-based approach ("give me items 50–100") breaks
// when rooms change rank between pages (a fast-growing room jumps from position
// 80 to position 10 between page 1 and page 2).
//
// Solution: the cursor encodes a snapshot of the ranking at the time of the
// first request. Subsequent pages decode the snapshot and slice from it.
// The cursor expires after 5 minutes — after that, the user gets a fresh feed.

// CursorPayload is the data encoded in the opaque cursor string.
type CursorPayload struct {
	// RankedIDs is the complete ordered list of room IDs from the initial ranking.
	// This snapshot makes pagination stable even as live viewer counts change.
	RankedIDs []string  `json:"ids"`
	Offset    int       `json:"off"`
	Tab       Tab       `json:"tab"`
	CreatedAt time.Time `json:"ts"`
}

const cursorTTL = 5 * time.Minute

// EncodeCursor serialises a cursor payload to an opaque string.
func EncodeCursor(p CursorPayload) string {
	b, _ := json.Marshal(p)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor deserialises an opaque cursor. Returns nil if expired or malformed.
func DecodeCursor(raw string) *CursorPayload {
	if raw == "" {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	var p CursorPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return nil
	}
	if time.Since(p.CreatedAt) > cursorTTL {
		return nil // expired — caller must request a fresh page
	}
	return &p
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
