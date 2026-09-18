package modsvc

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lumena/moderation"
)

// ─── Injected closures the risk engine needs ────────────────────────────────

type AgeAssuredFunc func(ctx context.Context, accountID string) (bool, error)
type AccountCreatedAtFunc func(ctx context.Context, accountID string) (time.Time, error)
type FollowerCountFunc func(ctx context.Context, accountID string) (int, error)
type IsFollowingFunc func(ctx context.Context, followerID, followeeID string) (bool, error)

// Dev-scale thresholds for doc 10 §2d's signals — not a tuned production
// model, same caveat as the RiskScore thresholds in moderation.go.
const (
	recentlyRegisteredWindow = 7 * 24 * time.Hour
	lowFollowerThreshold     = 10
	multiContactWindow       = 24 * time.Hour
	multiContactThreshold    = 3
	rapidEscalationWindow    = 10 * time.Minute
)

var groomingKeywords = []string{"send pics", "our secret", "don't tell", "meet in person", "how old are you really"}

// RiskEngine implements doc 10 §2d's grooming-pattern signal scoring. It
// only ever writes a moderation.RiskProfile — never creates an Enforcement.
// "No single signal triggers action; scored together."
type RiskEngine struct {
	repo RiskRepo

	ageAssured   AgeAssuredFunc
	createdAt    AccountCreatedAtFunc
	followerCnt  FollowerCountFunc
	isFollowing  IsFollowingFunc

	mu             sync.Mutex
	recentContacts map[string]map[string]time.Time // senderID -> recipientID -> last qualifying contact
	firstContact   map[[2]string]time.Time         // (senderID, recipientID) -> first DM ever
}

func NewRiskEngine(repo RiskRepo, ageAssured AgeAssuredFunc, createdAt AccountCreatedAtFunc, followerCnt FollowerCountFunc, isFollowing IsFollowingFunc) *RiskEngine {
	return &RiskEngine{
		repo: repo, ageAssured: ageAssured, createdAt: createdAt, followerCnt: followerCnt, isFollowing: isFollowing,
		recentContacts: make(map[string]map[string]time.Time),
		firstContact:   make(map[[2]string]time.Time),
	}
}

// AddSignal is the exported entry point other modules use to contribute to
// this SAME risk score — doc 12 Phase 18's "risk scoring pipeline" is one
// pipeline, not a second parallel one; the fraud module's device-clustering,
// gift-loop, and chargeback signals (AF-02/03/05) all land here, right next
// to the grooming-pattern signals this engine already computes (Phase 15,
// doc 10 §2d). Like every signal in this engine, it only ever writes a
// RiskProfile — it has no path to modsvc.Service.CreateEnforcement.
func (e *RiskEngine) AddSignal(ctx context.Context, accountID string, delta float64, reason string) {
	e.addSignal(ctx, accountID, delta, reason)
}

func (e *RiskEngine) addSignal(ctx context.Context, accountID string, delta float64, reason string) {
	p, err := e.repo.Get(ctx, accountID)
	if err != nil {
		return
	}
	found := false
	for _, r := range p.RiskReasons {
		if r == reason {
			found = true
			break
		}
	}
	if !found {
		p.RiskReasons = append(p.RiskReasons, reason)
	}
	p.RiskScore += delta
	switch {
	case p.RiskScore >= moderation.RiskManualReviewThreshold:
		p.ReviewStatus = moderation.ReviewManualReview
	case p.RiskScore >= moderation.RiskWatchThreshold:
		if p.ReviewStatus == moderation.ReviewNone {
			p.ReviewStatus = moderation.ReviewWatch
		}
	}
	p.UpdatedAt = time.Now()
	_ = e.repo.Upsert(ctx, *p)
}

// RecordMessage observes one direct message for the "adult contacting
// multiple recently-registered low-follower accounts" and keyword signals.
func (e *RiskEngine) RecordMessage(ctx context.Context, senderID, recipientID, body string) {
	e.mu.Lock()
	if _, ok := e.firstContact[[2]string{senderID, recipientID}]; !ok {
		e.firstContact[[2]string{senderID, recipientID}] = time.Now()
	}
	e.mu.Unlock()

	lower := strings.ToLower(body)
	for _, kw := range groomingKeywords {
		if strings.Contains(lower, kw) {
			e.addSignal(ctx, senderID, 2, "keyword_signal")
			break
		}
	}

	adult, err := e.ageAssured(ctx, senderID)
	if err != nil || !adult {
		return
	}
	createdAt, err := e.createdAt(ctx, recipientID)
	if err != nil || time.Since(createdAt) > recentlyRegisteredWindow {
		return
	}
	followers, err := e.followerCnt(ctx, recipientID)
	if err != nil || followers >= lowFollowerThreshold {
		return
	}

	e.mu.Lock()
	if e.recentContacts[senderID] == nil {
		e.recentContacts[senderID] = make(map[string]time.Time)
	}
	e.recentContacts[senderID][recipientID] = time.Now()
	cutoff := time.Now().Add(-multiContactWindow)
	count := 0
	for rid, at := range e.recentContacts[senderID] {
		if at.Before(cutoff) {
			delete(e.recentContacts[senderID], rid)
			continue
		}
		count++
	}
	e.mu.Unlock()

	if count >= multiContactThreshold {
		e.addSignal(ctx, senderID, 3, "multi_low_follower_contact")
	}
}

// RecordPVRequest observes a private-video request for the "rapid
// escalation from chat to PV" and "no reciprocal engagement" signals.
func (e *RiskEngine) RecordPVRequest(ctx context.Context, callerID, calleeID string) {
	e.mu.Lock()
	first, hadContact := e.firstContact[[2]string{callerID, calleeID}]
	e.mu.Unlock()

	if hadContact && time.Since(first) < rapidEscalationWindow {
		e.addSignal(ctx, callerID, 3, "rapid_escalation_to_pv")
	}

	if e.isFollowing == nil {
		return
	}
	reciprocal, err := e.isFollowing(ctx, calleeID, callerID)
	if err == nil && !reciprocal {
		e.addSignal(ctx, callerID, 2, "no_reciprocal_engagement")
	}
}
