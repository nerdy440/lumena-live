// Package referralsvc implements the referral service, following the same
// DI-via-closures pattern as every other module in this workspace:
// account existence and coin-crediting are injected as plain functions so
// this package never needs to import auth or ledger directly.
package referralsvc

import (
	"context"
	"time"

	"github.com/lumena/referral"
)

// AccountExistsFunc reports whether accountID is a real, registered
// account — a referral code is just an account ID, so redeeming an
// unknown code must fail cleanly rather than silently rewarding nobody.
type AccountExistsFunc func(ctx context.Context, accountID string) (bool, error)

// CreditCoinsFunc posts amount coins to accountID via the real ledger
// (never a direct balance edit) and returns the transaction ID.
type CreditCoinsFunc func(ctx context.Context, accountID string, amount int64, idempotencyKey string) (transactionID string, err error)

type Service struct {
	repo          referral.Repo
	accountExists AccountExistsFunc
	creditCoins   CreditCoinsFunc
}

func New(repo referral.Repo, accountExists AccountExistsFunc, creditCoins CreditCoinsFunc) *Service {
	return &Service{repo: repo, accountExists: accountExists, creditCoins: creditCoins}
}

// ClaimCode redeems referrerCode (a referrer's account ID) on behalf of
// referredID, crediting the referrer referral.RewardCoins.
//
// Ordering is deliberate and load-bearing: TryClaim (the atomic
// one-time gate) happens FIRST, strictly before any money moves; only if
// that succeeds do we post the ledger reward; only if THAT succeeds do
// we append the history record. This is the only ordering that can never
// double-credit a referrer (a concurrent double-claim is rejected by the
// gate before either request reaches the ledger) — the tradeoff is that
// a ledger failure after the gate closes "wastes" that account's one
// claim rather than money silently duplicating, which is the direction
// every other financial flow in this codebase also errs toward.
func (s *Service) ClaimCode(ctx context.Context, referredID, referrerCode string) (*referral.Referral, error) {
	if referredID == referrerCode {
		return nil, referral.ErrSelfReferral
	}
	exists, err := s.accountExists(ctx, referrerCode)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, referral.ErrCodeNotFound
	}

	ok, err := s.repo.TryClaim(ctx, referredID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, referral.ErrAlreadyClaimed
	}

	idempotencyKey := "referral-" + referredID
	txID, err := s.creditCoins(ctx, referrerCode, referral.RewardCoins, idempotencyKey)
	if err != nil {
		return nil, err
	}

	r := &referral.Referral{
		ReferrerID:    referrerCode,
		ReferredID:    referredID,
		RewardCoins:   referral.RewardCoins,
		TransactionID: txID,
		CreatedAt:     time.Now(),
	}
	if err := s.repo.RecordReward(ctx, r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) ListMyReferrals(ctx context.Context, referrerID string) ([]referral.Referral, error) {
	return s.repo.ListByReferrer(ctx, referrerID)
}
