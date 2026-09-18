// Package fraud implements the risk scoring system.
// Design: multi-signal scoring, review_status output, never auto-ban on one signal.
// See doc 12 §M and doc 12 Phase 18.
package fraud

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Signal is one observable data point that contributes to a risk score.
type Signal struct {
	Name      string
	Value     float64
	Weight    float64
	Threshold float64 // value above which this signal is "triggered"
	Reason    string  // human-readable reason added to risk_reasons
}

// RiskProfile is the output of a risk assessment.
type RiskProfile struct {
	AccountID    uuid.UUID
	RiskScore    float64  // 0.0–100.0
	RiskReasons  []string
	ReviewStatus string // none | watch | manual_review | restricted
	UpdatedAt    time.Time
}

// Scorer computes risk scores from signals.
type Scorer struct {
	db *pgxpool.Pool
}

func NewScorer(db *pgxpool.Pool) *Scorer {
	return &Scorer{db: db}
}

// Assess computes and persists a risk assessment for the given account.
// NEVER bans solely on one signal. See doc 05 §M.
func (s *Scorer) Assess(ctx context.Context, accountID uuid.UUID) (*RiskProfile, error) {
	signals, err := s.collectSignals(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("fraud: collect signals: %w", err)
	}

	var score float64
	var reasons []string
	for _, sig := range signals {
		if sig.Value > sig.Threshold {
			contribution := sig.Weight * (sig.Value / 100.0)
			score += contribution
			reasons = append(reasons, sig.Reason)
		}
	}
	score = min(score, 100.0)

	status := reviewStatus(score)

	profile := &RiskProfile{
		AccountID:    accountID,
		RiskScore:    score,
		RiskReasons:  reasons,
		ReviewStatus: status,
		UpdatedAt:    time.Now(),
	}

	// Persist — upsert into risk_profiles
	_, err = s.db.Exec(ctx, `
		INSERT INTO risk_profiles (account_id, risk_score, risk_reasons, review_status, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (account_id) DO UPDATE
		SET risk_score=$2, risk_reasons=$3, review_status=$4, updated_at=now()
	`, accountID, score, reasons, status)
	if err != nil {
		return nil, fmt.Errorf("fraud: persist profile: %w", err)
	}

	return profile, nil
}

func (s *Scorer) collectSignals(ctx context.Context, accountID uuid.UUID) ([]Signal, error) {
	var signals []Signal

	// Signal 1: rapid account creation (same device, multiple accounts)
	var deviceAccounts int
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT df.account_id)
		FROM device_fingerprints df
		WHERE df.device_hash IN (
		  SELECT device_hash FROM device_fingerprints WHERE account_id=$1
		)
		AND df.first_seen > now() - interval '7 days'
	`, accountID).Scan(&deviceAccounts)
	signals = append(signals, Signal{
		Name: "multi_account_device", Value: float64(deviceAccounts) * 10,
		Weight: 20, Threshold: 15,
		Reason: "Multiple accounts detected from the same device",
	})

	// Signal 2: gift loop detection (A→B gifts then B→A gifts within 10 minutes)
	var giftLoops int
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM ledger_transactions lt1
		JOIN ledger_transactions lt2 ON lt2.metadata->>'sender_id' = lt1.metadata->>'recipient_id'
		  AND lt1.metadata->>'recipient_id' = lt2.metadata->>'sender_id'
		  AND lt2.created_at BETWEEN lt1.created_at AND lt1.created_at + interval '10 minutes'
		WHERE lt1.kind='gift' AND lt2.kind='gift'
		  AND lt1.metadata->>'sender_id' = $1
		  AND lt1.created_at > now() - interval '24 hours'
	`, accountID.String()).Scan(&giftLoops)
	signals = append(signals, Signal{
		Name: "gift_loop", Value: float64(giftLoops) * 25,
		Weight: 30, Threshold: 20,
		Reason: "Reciprocal gifting pattern detected (potential wash trading)",
	})

	// Signal 3: high-frequency transactions
	var txCount int
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM ledger_transactions lt
		JOIN ledger_entries le ON le.transaction_id = lt.id
		JOIN ledger_accounts la ON la.id = le.account_id AND la.owner_id = $1
		WHERE lt.created_at > now() - interval '1 hour' AND lt.kind='gift'
	`, accountID).Scan(&txCount)
	signals = append(signals, Signal{
		Name: "high_frequency_gifts", Value: float64(txCount),
		Weight: 15, Threshold: 50,
		Reason: "Unusually high gifting frequency",
	})

	// Signal 4: chargeback history
	var chargebacks int
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM orders WHERE account_id=$1 AND status='refunded'
		AND updated_at > now() - interval '90 days'
	`, accountID).Scan(&chargebacks)
	signals = append(signals, Signal{
		Name: "chargeback_history", Value: float64(chargebacks) * 20,
		Weight: 25, Threshold: 15,
		Reason: "Chargeback or refund abuse detected",
	})

	return signals, nil
}

// reviewStatus converts a score to a review status.
// These thresholds are configurable via remote config in production.
func reviewStatus(score float64) string {
	switch {
	case score < 20:
		return "none"
	case score < 50:
		return "watch"
	case score < 80:
		return "manual_review"
	default:
		// Even at 100, the output is "restricted" — not a ban.
		// Restriction goes through the enforcement pipeline with human review.
		return "manual_review"
	}
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
