// Package pvsvc implements the private-video call gate chain, payment,
// and lifecycle: request -> accept/decline -> end, mirroring the same
// coin-economics pattern as ledger/giftsvc (gift sends) and
// feed/feedsvc's premium-room unlock (pay once, platform takes a cut,
// the other party earns diamonds).
package pvsvc

import (
	"context"
	"errors"
	"time"

	"github.com/lumena/ledger"
	"github.com/lumena/pv"
)

var (
	ErrSelfCall           = errors.New("pvsvc: cannot call yourself")
	ErrAccountNotActive   = errors.New("pvsvc: account is not active")
	ErrAgeNotAssured      = errors.New("pvsvc: age must be assured to use private video")
	ErrCalleeBlocked      = errors.New("pvsvc: blocked")
	ErrCalleeNotReachable = errors.New("pvsvc: this user isn't accepting calls right now")
	ErrSessionNotFound    = errors.New("pvsvc: session not found")
	ErrNotParticipant     = pv.ErrNotParticipant
	ErrInvalidState       = pv.ErrInvalidState
)

// platformTakeBPS/diamondsPerThousandCoins mirror the constants in
// ledger/giftsvc/service.go and feed/cmd/api/main.go's premium-unlock
// closure exactly — every pay-someone-coins feature in this codebase
// uses the same split.
const (
	platformTakeBPS          = 3000
	diamondsPerThousandCoins = 700
)

type AccountActiveFunc func(ctx context.Context, accountID string) (bool, error)
type AgeAssuredFunc func(ctx context.Context, accountID string) (bool, error)
type IsBlockedFunc func(ctx context.Context, a, b string) (bool, error)
type CalleePermitsFunc func(ctx context.Context, callerID, calleeID string) (bool, error)
type PublishFunc func(topic, eventType string, payload map[string]any)

type Service struct {
	repo          pv.Repo
	ledger        ledger.Repo
	accountActive AccountActiveFunc
	ageAssured    AgeAssuredFunc
	isBlocked     IsBlockedFunc
	calleePermits CalleePermitsFunc
	publish       PublishFunc
	onRequested   func(ctx context.Context, callerID, calleeID string)
}

func NewService(
	repo pv.Repo, ledgerRepo ledger.Repo,
	accountActive AccountActiveFunc, ageAssured AgeAssuredFunc,
	isBlocked IsBlockedFunc, calleePermits CalleePermitsFunc, publish PublishFunc,
) *Service {
	return &Service{
		repo: repo, ledger: ledgerRepo,
		accountActive: accountActive, ageAssured: ageAssured,
		isBlocked: isBlocked, calleePermits: calleePermits, publish: publish,
	}
}

// WithRequestedHook feeds real PV request traffic into the moderation risk
// engine without this package importing moderation (Phase 15's DI
// convention — same pattern as chatsvc.WithMessageHook).
func (s *Service) WithRequestedHook(fn func(ctx context.Context, callerID, calleeID string)) *Service {
	s.onRequested = fn
	return s
}

// chargeCoins debits payerID coins and credits payeeID the equivalent
// diamonds (minus the platform's cut) as one balanced transaction —
// identical shape to giftsvc.SendGift's entries and feed/cmd/api's
// premium-unlock closure.
func (s *Service) chargeCoins(ctx context.Context, payerID, payeeID string, coins int64, kind, idempotencyKey string, metadata map[string]any) (*ledger.Transaction, error) {
	platformShare := coins * platformTakeBPS / 10000
	payeeCoinsEquivalent := coins - platformShare
	payeeDiamonds := payeeCoinsEquivalent * diamondsPerThousandCoins / 1000
	entries := []ledger.Entry{
		{AccountID: ledger.UserCoinsAccount(payerID), Amount: -coins, Currency: ledger.Coin},
		{AccountID: ledger.PlatformRevenueAccount, Amount: platformShare, Currency: ledger.Coin},
		{AccountID: ledger.PlatformLiabilityAccount, Amount: payeeCoinsEquivalent, Currency: ledger.Coin},
		{AccountID: ledger.UserDiamondsAccount(payeeID), Amount: payeeDiamonds, Currency: ledger.Diamond},
		{AccountID: ledger.PlatformDiamondLiabilityAccount, Amount: -payeeDiamonds, Currency: ledger.Diamond},
	}
	tx, _, err := s.ledger.PostTransaction(ctx, kind, idempotencyKey, metadata, entries)
	return tx, err
}

// RequestSession runs the gate chain (active account, age-assured, not
// blocked, callee's WhoCanCall policy) and — same as premium rooms —
// charges the FIRST MINUTE upfront before the callee ever rings. This is
// both the anti-troll "reserve before ring" gate and, for the client, the
// exact moment an insufficient balance surfaces as *ledger.InsufficientBalanceError
// so the caller can be routed to a top-up screen before wasting the
// callee's time.
func (s *Service) RequestSession(ctx context.Context, callerID, calleeID string, ratePerMinCoins int64, idempotencyKey string) (*pv.Session, bool, error) {
	if callerID == calleeID {
		return nil, false, ErrSelfCall
	}
	if ratePerMinCoins <= 0 {
		ratePerMinCoins = 10
	}
	if existing, _ := s.ledger.GetTransaction(ctx, idempotencyKey); existing != nil {
		if sessionID, ok := existing.Metadata["session_id"].(string); ok {
			if sess, _ := s.repo.Get(ctx, sessionID); sess != nil {
				return sess, false, nil
			}
		}
	}
	if ok, err := s.accountActive(ctx, callerID); err != nil {
		return nil, false, err
	} else if !ok {
		return nil, false, ErrAccountNotActive
	}
	if ok, err := s.ageAssured(ctx, callerID); err != nil {
		return nil, false, err
	} else if !ok {
		return nil, false, ErrAgeNotAssured
	}
	if s.isBlocked != nil {
		if blocked, err := s.isBlocked(ctx, callerID, calleeID); err != nil {
			return nil, false, err
		} else if blocked {
			return nil, false, ErrCalleeBlocked
		}
	}
	if s.calleePermits != nil {
		if ok, err := s.calleePermits(ctx, callerID, calleeID); err != nil {
			return nil, false, err
		} else if !ok {
			return nil, false, ErrCalleeNotReachable
		}
	}

	sess := &pv.Session{
		ID: pv.NewSessionID(), CallerID: callerID, CalleeID: calleeID,
		State: pv.StateRequested, RatePerMinCoins: ratePerMinCoins, CreatedAt: time.Now(),
	}
	metadata := map[string]any{
		"session_id": sess.ID, "caller_id": callerID, "callee_id": calleeID, "kind": "first_minute_hold",
	}
	holdTx, err := s.chargeCoins(ctx, callerID, calleeID, ratePerMinCoins, "pv_call", idempotencyKey, metadata)
	if err != nil {
		return nil, false, err
	}
	sess.ConsumedCoins = ratePerMinCoins
	sess.HoldTransactionID = holdTx.ID
	if err := s.repo.Create(ctx, sess); err != nil {
		return nil, false, err
	}

	if s.onRequested != nil {
		s.onRequested(ctx, callerID, calleeID)
	}
	if s.publish != nil {
		s.publish("user:"+calleeID, "PV_REQUEST", map[string]any{
			"session_id": sess.ID, "caller_id": callerID, "rate_coins_per_min": ratePerMinCoins,
		})
	}
	return sess, true, nil
}

func (s *Service) GetSession(ctx context.Context, accountID, sessionID string) (*pv.Session, error) {
	sess, err := s.repo.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, ErrSessionNotFound
	}
	if !sess.HasParticipant(accountID) {
		return nil, ErrNotParticipant
	}
	return sess, nil
}

func (s *Service) ListSessions(ctx context.Context, accountID string) ([]pv.Session, error) {
	return s.repo.ListByAccount(ctx, accountID)
}

// AcceptSession transitions a requested call into an active one. Only the
// callee may accept.
func (s *Service) AcceptSession(ctx context.Context, calleeID, sessionID string) (*pv.Session, error) {
	sess, err := s.repo.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, ErrSessionNotFound
	}
	if sess.CalleeID != calleeID {
		return nil, ErrNotParticipant
	}
	if sess.State != pv.StateRequested {
		return nil, ErrInvalidState
	}
	now := time.Now()
	sess.State = pv.StateAccepted
	sess.AcceptedAt = &now
	if err := s.repo.Update(ctx, sess); err != nil {
		return nil, err
	}
	if s.publish != nil {
		s.publish("user:"+sess.CallerID, "PV_ACCEPTED", map[string]any{"session_id": sess.ID})
	}
	return sess, nil
}

// DeclineSession transitions a requested call to declined and refunds the
// first-minute hold charged at request time — the callee never has to
// justify saying no, and the caller never pays for a call nobody took.
func (s *Service) DeclineSession(ctx context.Context, calleeID, sessionID string) (*pv.Session, error) {
	sess, err := s.repo.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, ErrSessionNotFound
	}
	if sess.CalleeID != calleeID {
		return nil, ErrNotParticipant
	}
	if sess.State != pv.StateRequested {
		return nil, ErrInvalidState
	}
	now := time.Now()
	sess.State = pv.StateDeclined
	sess.EndedAt = &now
	s.refundHold(ctx, sess)
	if err := s.repo.Update(ctx, sess); err != nil {
		return nil, err
	}
	if s.publish != nil {
		s.publish("user:"+sess.CallerID, "PV_DECLINED", map[string]any{"session_id": sess.ID})
	}
	return sess, nil
}

// EndSession settles final billing and closes the session — either party
// may end an active call, and either may cancel one still ringing.
func (s *Service) EndSession(ctx context.Context, accountID, sessionID string) (*pv.Session, error) {
	sess, err := s.repo.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, ErrSessionNotFound
	}
	if !sess.HasParticipant(accountID) {
		return nil, ErrNotParticipant
	}
	if sess.State != pv.StateRequested && sess.State != pv.StateAccepted {
		return nil, ErrInvalidState
	}

	now := time.Now()
	wasAccepted := sess.State == pv.StateAccepted
	sess.State = pv.StateEnded
	sess.EndedAt = &now

	if wasAccepted && sess.AcceptedAt != nil {
		s.settleElapsed(ctx, sess, now)
	} else {
		s.refundHold(ctx, sess)
	}

	if err := s.repo.Update(ctx, sess); err != nil {
		return nil, err
	}
	other := sess.CalleeID
	if accountID == sess.CalleeID {
		other = sess.CallerID
	}
	if s.publish != nil {
		s.publish("user:"+other, "PV_ENDED", map[string]any{"session_id": sess.ID, "ended_by": accountID})
	}
	return sess, nil
}

// settleElapsed reverses the flat first-minute hold and re-charges the
// caller for the call's actual connected duration, rounded up to the
// nearest full minute (minimum one) — the hold was only ever a "can you
// afford to start this" gate, not the real bill. Best-effort: if the
// caller can no longer afford the true total (spent coins elsewhere
// mid-call), the call still ends — a failed top-up charge must never
// trap two people in a call neither can hang up on.
func (s *Service) settleElapsed(ctx context.Context, sess *pv.Session, endedAt time.Time) {
	elapsed := endedAt.Sub(*sess.AcceptedAt)
	minutes := int64(elapsed / time.Minute)
	if elapsed%time.Minute > 0 || minutes == 0 {
		minutes++
	}
	trueCost := minutes * sess.RatePerMinCoins

	s.reverseHold(ctx, sess, "pv_call settlement")
	sess.ConsumedCoins = 0

	metadata := map[string]any{
		"session_id": sess.ID, "caller_id": sess.CallerID, "callee_id": sess.CalleeID,
		"kind": "final_settlement", "minutes": minutes,
	}
	if _, err := s.chargeCoins(ctx, sess.CallerID, sess.CalleeID, trueCost, "pv_call", "pv-settle-"+sess.ID, metadata); err == nil {
		sess.ConsumedCoins = trueCost
	}
	// On error (insufficient balance), ConsumedCoins stays 0 — the caller
	// was refunded the hold and simply isn't charged further; the call
	// still ends cleanly either way.
}

func (s *Service) refundHold(ctx context.Context, sess *pv.Session) {
	s.reverseHold(ctx, sess, "pv_call not connected")
	sess.ConsumedCoins = 0
}

func (s *Service) reverseHold(ctx context.Context, sess *pv.Session, reason string) {
	if sess.HoldTransactionID == "" {
		return
	}
	_, _, _ = s.ledger.ReverseTransaction(ctx, sess.HoldTransactionID, "pv-hold-reverse-"+sess.ID, reason)
}
