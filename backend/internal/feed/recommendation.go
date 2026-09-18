// Package feed implements the discovery and recommendation system.
// Architecture: RecommendationEngine interface with RuleBasedRecommendationEngine as MVP.
// The ML implementation is designed for in doc 05 §L and slotted for Phase 17.
package feed

import (
	"context"
	"math"
	"sort"
	"time"
)

// RoomCandidate is a live room eligible for recommendation.
type RoomCandidate struct {
	RoomID      string
	HostID      string
	Language    string
	RegionCode  string
	Tags        []string
	ViewerCount int
	GiftVelocity float64 // gifts per minute (server-side computed, not exposed to client)
	StartedAt   time.Time
	PeakViewers int
	FollowerGrowthRate float64 // new follows per minute
}

// UserContext carries the signals we use to personalize.
type UserContext struct {
	AccountID      string
	Languages      []string
	Interests      []string
	RegionCode     string
	FollowedHostIDs []string
	RecentlyViewedRoomIDs []string
}

// ScoredRoom is a candidate with its computed score.
type ScoredRoom struct {
	RoomCandidate
	Score float64
}

// RecommendationEngine is the interface both implementations satisfy.
type RecommendationEngine interface {
	Rank(ctx context.Context, candidates []RoomCandidate, user UserContext, n int) ([]ScoredRoom, error)
	Name() string
}

// ─────────────────────────────────────────────
// RuleBasedRecommendationEngine — MVP (Phase 4)
// ─────────────────────────────────────────────
//
// Scoring formula (see doc 05 §L):
//   score = freshness + engagement + watch_time_proxy + viewer_growth
//           + creator_quality + follow_probability + gift_activity
//           + language_match + region_relevance
//
// This is NOT just sorted by viewer count — that's the competitor's approach
// and produces a rich-get-richer dynamic that buries new creators.

type RuleBasedRecommendationEngine struct {
	weights RuleWeights
}

type RuleWeights struct {
	Freshness         float64
	Engagement        float64
	ViewerGrowth      float64
	GiftActivity      float64
	LanguageMatch     float64
	RegionRelevance   float64
	FollowBoost       float64
	RecentlyViewedPen float64 // penalty for recently viewed rooms
}

func DefaultWeights() RuleWeights {
	return RuleWeights{
		Freshness:         0.15,
		Engagement:        0.20,
		ViewerGrowth:      0.15,
		GiftActivity:      0.15,
		LanguageMatch:     0.15,
		RegionRelevance:   0.10,
		FollowBoost:       0.15,
		RecentlyViewedPen: 0.50, // penalty applied multiplicatively
	}
}

func NewRuleBasedEngine(weights RuleWeights) *RuleBasedRecommendationEngine {
	return &RuleBasedRecommendationEngine{weights: weights}
}

func (e *RuleBasedRecommendationEngine) Name() string { return "rule-based-v1" }

func (e *RuleBasedRecommendationEngine) Rank(
	_ context.Context,
	candidates []RoomCandidate,
	user UserContext,
	n int,
) ([]ScoredRoom, error) {
	w := e.weights
	langSet := toSet(user.Languages)
	followSet := toSet(user.FollowedHostIDs)
	viewedSet := toSet(user.RecentlyViewedRoomIDs)

	scored := make([]ScoredRoom, 0, len(candidates))
	for _, c := range candidates {
		s := 0.0

		// Freshness: exponential decay; full score if <15min, half at 60min
		ageMin := time.Since(c.StartedAt).Minutes()
		freshness := math.Exp(-0.012 * ageMin) // half-life ~58 min
		s += w.Freshness * freshness

		// Engagement: log-normalized viewer count (avoids raw-count dominance)
		// sqrt(viewers)/sqrt(maxViewers) to smooth out outliers
		engagement := math.Log1p(float64(c.ViewerCount)) / math.Log1p(50000)
		s += w.Engagement * clamp(engagement, 0, 1)

		// Viewer growth rate (signal of momentum, not just current size)
		growth := clamp(c.FollowerGrowthRate/10.0, 0, 1)
		s += w.ViewerGrowth * growth

		// Gift activity: proxy for creator quality and room energy
		giftScore := clamp(c.GiftVelocity/5.0, 0, 1)
		s += w.GiftActivity * giftScore

		// Language match
		if langSet[c.Language] {
			s += w.LanguageMatch * 1.0
		} else {
			s += w.LanguageMatch * 0.2 // partial credit — discovery across languages
		}

		// Region relevance
		if c.RegionCode == user.RegionCode {
			s += w.RegionRelevance * 1.0
		} else {
			s += w.RegionRelevance * 0.3
		}

		// Follow boost: user follows this host → strong signal
		if followSet[c.HostID] {
			s += w.FollowBoost * 1.0
		}

		// Recently viewed penalty (avoid showing the same room again)
		if viewedSet[c.RoomID] {
			s *= (1.0 - w.RecentlyViewedPen)
		}

		scored = append(scored, ScoredRoom{RoomCandidate: c, Score: s})
	}

	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })

	if n > 0 && n < len(scored) {
		scored = scored[:n]
	}
	return scored, nil
}

// ─────────────────────────────────────────────
// MLRecommendationEngine — Phase 17 (interface designed now)
// ─────────────────────────────────────────────

// MLRecommendationEngine satisfies the same interface.
// NOT IMPLEMENTED — Phase 17. The interface design here ensures that
// swapping from rule-based to ML requires no changes in the callers.
type MLRecommendationEngine struct{}

func (m *MLRecommendationEngine) Name() string { return "ml-v1" }
func (m *MLRecommendationEngine) Rank(_ context.Context, candidates []RoomCandidate, _ UserContext, _ int) ([]ScoredRoom, error) {
	// NOT IMPLEMENTED — Phase 17.
	// When implemented: batch inference call to the model service,
	// with the rule-based engine as the fallback if the model is unavailable.
	return nil, nil
}

// ─────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────

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
