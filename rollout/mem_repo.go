package rollout

import (
	"context"
	"sort"
	"sync"
	"time"
)

// ─── Flags ──────────────────────────────────────────────────────────────────

type MemFlagRepo struct {
	mu    sync.Mutex
	flags map[string]*FeatureFlag
}

func NewMemFlagRepo() *MemFlagRepo {
	return &MemFlagRepo{flags: make(map[string]*FeatureFlag)}
}

func (r *MemFlagRepo) Get(_ context.Context, key string) (*FeatureFlag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.flags[key]
	if !ok {
		return nil, ErrFlagNotFound
	}
	cp := *f
	return &cp, nil
}

// Upsert creates the flag at StageOff if it doesn't exist yet, or updates
// its stage if it does.
func (r *MemFlagRepo) Upsert(_ context.Context, key string, stage Stage) (*FeatureFlag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	f, ok := r.flags[key]
	if !ok {
		f = &FeatureFlag{Key: key, CreatedAt: now}
		r.flags[key] = f
	}
	f.Stage = stage
	f.UpdatedAt = now
	cp := *f
	return &cp, nil
}

func (r *MemFlagRepo) List(_ context.Context) ([]FeatureFlag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FeatureFlag, 0, len(r.flags))
	for _, f := range r.flags {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ─── Internal/staff allowlist (dev-only escalation, no real staff
// directory exists) ─────────────────────────────────────────────────────

type MemInternalRepo struct {
	mu  sync.Mutex
	set map[string]bool
}

func NewMemInternalRepo() *MemInternalRepo {
	return &MemInternalRepo{set: make(map[string]bool)}
}

func (r *MemInternalRepo) IsInternal(_ context.Context, accountID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.set[accountID], nil
}

func (r *MemInternalRepo) MarkInternal(_ context.Context, accountID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.set[accountID] = true
	return nil
}

// ─── Metrics ────────────────────────────────────────────────────────────

type MemMetricsRepo struct {
	mu         sync.Mutex
	requests   map[Cohort]int64
	errors     map[Cohort]int64
	giftAtt    map[Cohort]int64
	giftOK     map[Cohort]int64
	panics     map[Cohort]int64
	latencies  map[Cohort][]time.Duration // capped ring, see recordLatency
}

const maxLatencySamples = 5000

func NewMemMetricsRepo() *MemMetricsRepo {
	return &MemMetricsRepo{
		requests: make(map[Cohort]int64), errors: make(map[Cohort]int64),
		giftAtt: make(map[Cohort]int64), giftOK: make(map[Cohort]int64),
		panics: make(map[Cohort]int64), latencies: make(map[Cohort][]time.Duration),
	}
}

func (r *MemMetricsRepo) RecordRequest(_ context.Context, cohort Cohort, status int, latency time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests[cohort]++
	if status >= 500 {
		r.errors[cohort]++
	}
	if len(r.latencies[cohort]) < maxLatencySamples {
		r.latencies[cohort] = append(r.latencies[cohort], latency)
	}
	return nil
}

func (r *MemMetricsRepo) RecordGiftAttempt(_ context.Context, cohort Cohort, success bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.giftAtt[cohort]++
	if success {
		r.giftOK[cohort]++
	}
	return nil
}

func (r *MemMetricsRepo) RecordPanic(_ context.Context, cohort Cohort) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.panics[cohort]++
	return nil
}

func (r *MemMetricsRepo) Snapshot(_ context.Context, cohort Cohort) (Metrics, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	lats := append([]time.Duration(nil), r.latencies[cohort]...)
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	pct := func(p float64) float64 {
		if len(lats) == 0 {
			return 0
		}
		idx := int(float64(len(lats)-1) * p)
		return float64(lats[idx].Microseconds()) / 1000.0
	}

	m := Metrics{
		Cohort: cohort, RequestCount: r.requests[cohort], ErrorCount: r.errors[cohort],
		P50LatencyMs: pct(0.50), P95LatencyMs: pct(0.95), P99LatencyMs: pct(0.99),
		GiftAttempts: r.giftAtt[cohort], GiftSuccesses: r.giftOK[cohort],
		PanicCount: r.panics[cohort],
	}
	if m.RequestCount > 0 {
		m.ErrorRatePct = float64(m.ErrorCount) / float64(m.RequestCount) * 100
		m.CrashRatePct = float64(m.PanicCount) / float64(m.RequestCount) * 100
	}
	if m.GiftAttempts > 0 {
		m.GiftSuccessRatePct = float64(m.GiftSuccesses) / float64(m.GiftAttempts) * 100
	}
	return m, nil
}
