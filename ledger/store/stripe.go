package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// StripeVerifier is a real Verifier implementation for Stripe Checkout: it
// calls Stripe's own API to confirm a Checkout Session actually completed,
// rather than trusting anything the client claims. purchaseToken is the
// Checkout Session ID ("cs_..."), which the client only ever receives from
// Stripe itself after a real checkout.
//
// This is the one piece of the purchase pipeline this codebase genuinely
// cannot fully exercise without a live Stripe account (doc rule 8: "do not
// create mock financial functionality disguised as production
// functionality") — so unlike every other DevXxx stand-in in this
// workspace, this is not a simulation. It performs a real authenticated
// HTTPS call and real response validation; it is simply untested against
// Stripe's live servers because no account/secret key exists in this dev
// environment. BaseURL is overridable so tests can point it at a fake
// Stripe server instead of stubbing Verify out entirely.
type StripeVerifier struct {
	SecretKey string
	BaseURL   string // defaults to https://api.stripe.com if empty
	Client    *http.Client
}

func NewStripeVerifier(secretKey string) *StripeVerifier {
	return &StripeVerifier{
		SecretKey: secretKey,
		BaseURL:   "https://api.stripe.com",
		Client:    &http.Client{Timeout: 10 * time.Second},
	}
}

var _ Verifier = (*StripeVerifier)(nil)

// checkoutSession is the subset of Stripe's Checkout Session object
// (https://stripe.com/docs/api/checkout/sessions/object) this verifier
// checks. A real checkout-session-creation call must set
// client_reference_id to the order ID when the session is created (before
// redirecting the client to Stripe) so that value round-trips back here —
// without it, a genuine session for order A could be replayed against
// order B even though both tokens are individually "real."
type checkoutSession struct {
	ID                 string `json:"id"`
	PaymentStatus      string `json:"payment_status"` // "paid" | "unpaid" | "no_payment_required"
	Status             string `json:"status"`          // "open" | "complete" | "expired"
	AmountTotal        int64  `json:"amount_total"`    // minor units, e.g. cents
	Currency           string `json:"currency"`        // lowercase ISO, e.g. "usd"
	ClientReferenceID  string `json:"client_reference_id"`
}

type stripeError struct {
	Error struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Verify only ever handles platform == "stripe" — other platform values
// (Google Play, App Store) need their own Verifier, wired by the
// composition root per platform rather than forced through this one.
func (v *StripeVerifier) Verify(ctx context.Context, platform, purchaseToken string, expected PurchaseExpectation) error {
	if platform != "stripe" {
		return fmt.Errorf("store: StripeVerifier cannot verify platform %q", platform)
	}
	if purchaseToken == "" {
		return ErrInvalidToken
	}

	url := strings.TrimRight(v.BaseURL, "/") + "/v1/checkout/sessions/" + purchaseToken
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(v.SecretKey, "")

	resp, err := v.Client.Do(req)
	if err != nil {
		// A transport-level failure (network blip, Stripe outage) is not
		// proof the token is invalid — surface it as pending so the
		// reconciler retries instead of permanently failing the order on a
		// transient error.
		return ErrPending
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrInvalidToken
	}
	if resp.StatusCode != http.StatusOK {
		var se stripeError
		_ = json.NewDecoder(resp.Body).Decode(&se)
		if se.Error.Type == "invalid_request_error" {
			return ErrInvalidToken
		}
		return fmt.Errorf("store: stripe returned %d: %s", resp.StatusCode, se.Error.Message)
	}

	var sess checkoutSession
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return fmt.Errorf("store: could not parse stripe response: %w", err)
	}

	switch sess.Status {
	case "open":
		return ErrPending
	case "expired":
		return ErrInvalidToken
	}
	if sess.PaymentStatus != "paid" && sess.PaymentStatus != "no_payment_required" {
		return ErrInvalidToken
	}

	// The core anti-fraud check: the session Stripe actually charged must
	// match what this order expects, not just any successful session.
	if sess.ClientReferenceID != expected.OrderID {
		return ErrInvalidToken
	}
	if sess.AmountTotal != expected.PriceMinor {
		return ErrInvalidToken
	}
	if !strings.EqualFold(sess.Currency, expected.PriceCurrency) {
		return ErrInvalidToken
	}

	return nil
}
