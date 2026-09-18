// Package rollout implements feature flags, staged rollout, and canary
// metrics (doc 02 PL-05, doc 07 §13's GET /config, roadmap Phase 20).
//
// Deployment mechanics this repo genuinely has none of — a build pipeline
// that ships two binaries (control + canary), a load balancer that can
// route traffic between them, an on-call paging tool, and the human
// processes (on-call rotation, legal sign-off by region) roadmap Phase 20
// also lists — are out of scope for a single `go run` dev process, same
// category of gap as Phase 19's CDN/Kafka/Postgres-cluster chaos tests.
// What this package implements for real: deterministic percentage-based
// cohort assignment (the actual mechanism a staged rollout runs on) and
// per-cohort metrics collection, both wired into the one real running
// server and testable end-to-end.
package rollout

import (
	"errors"
	"time"
)

// ─── Feature flags / staged rollout ────────────────────────────────────────

type Stage string

const (
	StageOff      Stage = "off"      // 0% — flag disabled entirely
	StageInternal Stage = "internal" // staff/internal accounts only
	Stage1Pct     Stage = "1pct"
	Stage5Pct     Stage = "5pct"
	Stage25Pct    Stage = "25pct"
	StageFull     Stage = "100pct"
)

// Stages is the ordered staged-rollout path (doc 12 Phase 20: "internal →
// 1% → 5% → 25% → 100%"), plus Off as the rollback target.
var Stages = []Stage{StageOff, StageInternal, Stage1Pct, Stage5Pct, Stage25Pct, StageFull}

// Percent returns the traffic percentage for stage (Internal resolves via
// the internal-accounts allowlist instead, so it isn't a percentage — 0
// is a safe default for anyone not on that list).
func Percent(s Stage) int {
	switch s {
	case Stage1Pct:
		return 1
	case Stage5Pct:
		return 5
	case Stage25Pct:
		return 25
	case StageFull:
		return 100
	default: // Off, Internal
		return 0
	}
}

func ValidStage(s Stage) bool {
	for _, st := range Stages {
		if st == s {
			return true
		}
	}
	return false
}

type FeatureFlag struct {
	Key       string    `json:"key"`
	Stage     Stage     `json:"stage"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

var (
	ErrFlagNotFound = errors.New("rollout: unknown flag key")
	ErrInvalidStage = errors.New("rollout: unknown rollout stage")
	ErrForbidden    = errors.New("rollout: rollout.manage permission required")
)

// ─── Canary metrics (doc 12 Phase 20: "error rate, p99 latency, gift
// success rate, crash rate") ────────────────────────────────────────────

// Cohort is which build/config a request is logically served under. This
// process only ever runs one binary — there is no second canary build to
// actually route to — so cohort tagging here is a deterministic
// percentage split of real traffic used purely to segment the metrics
// this same process already collects, the way a real canary comparison
// would, without a real second deployment behind it.
type Cohort string

const (
	CohortControl Cohort = "control"
	CohortCanary  Cohort = "canary"
)

// CanaryFlagKey is the designated flag whose evaluation decides a
// request's Cohort.
const CanaryFlagKey = "platform_canary_build"

type Metrics struct {
	Cohort        Cohort  `json:"cohort"`
	RequestCount  int64   `json:"request_count"`
	ErrorCount    int64   `json:"error_count"` // 5xx responses
	ErrorRatePct  float64 `json:"error_rate_pct"`
	P50LatencyMs  float64 `json:"p50_latency_ms"`
	P95LatencyMs  float64 `json:"p95_latency_ms"`
	P99LatencyMs  float64 `json:"p99_latency_ms"`
	GiftAttempts  int64   `json:"gift_attempts"`
	GiftSuccesses int64   `json:"gift_successes"`
	GiftSuccessRatePct float64 `json:"gift_success_rate_pct"`
	PanicCount    int64   `json:"panic_count"`
	CrashRatePct  float64 `json:"crash_rate_pct"` // panics / requests
}

type MetricsComparison struct {
	Control Metrics `json:"control"`
	Canary  Metrics `json:"canary"`
}
