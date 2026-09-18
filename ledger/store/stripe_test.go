package store_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lumena/ledger/store"
)

// fakeStripeServer stands in for Stripe's API — it never talks to the real
// internet, but StripeVerifier's HTTP call, auth header, and JSON
// (un)marshalling against it are exactly what would run against the real
// api.stripe.com.
func fakeStripeServer(t *testing.T, sessions map[string]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, ok := r.BasicAuth()
		if !ok || user == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		id := r.URL.Path[len("/v1/checkout/sessions/"):]
		sess, ok := sessions[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"type": "invalid_request_error", "message": "No such checkout session"},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(sess)
	}))
}

func TestStripeVerifier_ValidPaidSessionMatchingOrderSucceeds(t *testing.T) {
	srv := fakeStripeServer(t, map[string]map[string]any{
		"cs_test_1": {
			"id": "cs_test_1", "payment_status": "paid", "status": "complete",
			"amount_total": int64(499), "currency": "usd", "client_reference_id": "order-1",
		},
	})
	defer srv.Close()

	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: srv.URL, Client: srv.Client()}
	err := v.Verify(context.Background(), "stripe", "cs_test_1", store.PurchaseExpectation{
		OrderID: "order-1", PriceMinor: 499, PriceCurrency: "USD",
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestStripeVerifier_UnknownTokenIsInvalid(t *testing.T) {
	srv := fakeStripeServer(t, map[string]map[string]any{})
	defer srv.Close()

	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: srv.URL, Client: srv.Client()}
	err := v.Verify(context.Background(), "stripe", "cs_does_not_exist", store.PurchaseExpectation{OrderID: "order-1", PriceMinor: 499, PriceCurrency: "USD"})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestStripeVerifier_OpenSessionIsPending(t *testing.T) {
	srv := fakeStripeServer(t, map[string]map[string]any{
		"cs_test_2": {
			"id": "cs_test_2", "payment_status": "unpaid", "status": "open",
			"amount_total": int64(499), "currency": "usd", "client_reference_id": "order-2",
		},
	})
	defer srv.Close()

	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: srv.URL, Client: srv.Client()}
	err := v.Verify(context.Background(), "stripe", "cs_test_2", store.PurchaseExpectation{OrderID: "order-2", PriceMinor: 499, PriceCurrency: "USD"})
	if err != store.ErrPending {
		t.Fatalf("expected ErrPending for an open (not yet paid) session, got %v", err)
	}
}

func TestStripeVerifier_AmountMismatchRejected(t *testing.T) {
	// A genuinely paid session — but for less than this order expects.
	// Without the amount check, a client could pay for the cheapest SKU and
	// replay that token against an order for an expensive one.
	srv := fakeStripeServer(t, map[string]map[string]any{
		"cs_test_3": {
			"id": "cs_test_3", "payment_status": "paid", "status": "complete",
			"amount_total": int64(99), "currency": "usd", "client_reference_id": "order-3",
		},
	})
	defer srv.Close()

	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: srv.URL, Client: srv.Client()}
	err := v.Verify(context.Background(), "stripe", "cs_test_3", store.PurchaseExpectation{OrderID: "order-3", PriceMinor: 1999, PriceCurrency: "USD"})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for amount mismatch, got %v", err)
	}
}

func TestStripeVerifier_OrderIDMismatchRejected(t *testing.T) {
	// A genuinely paid session for a DIFFERENT order — the classic
	// token-replay-across-orders attack this check exists to block.
	srv := fakeStripeServer(t, map[string]map[string]any{
		"cs_test_4": {
			"id": "cs_test_4", "payment_status": "paid", "status": "complete",
			"amount_total": int64(499), "currency": "usd", "client_reference_id": "order-belongs-to-someone-else",
		},
	})
	defer srv.Close()

	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: srv.URL, Client: srv.Client()}
	err := v.Verify(context.Background(), "stripe", "cs_test_4", store.PurchaseExpectation{OrderID: "order-4", PriceMinor: 499, PriceCurrency: "USD"})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for order id mismatch, got %v", err)
	}
}

func TestStripeVerifier_CurrencyMismatchRejected(t *testing.T) {
	srv := fakeStripeServer(t, map[string]map[string]any{
		"cs_test_5": {
			"id": "cs_test_5", "payment_status": "paid", "status": "complete",
			"amount_total": int64(499), "currency": "eur", "client_reference_id": "order-5",
		},
	})
	defer srv.Close()

	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: srv.URL, Client: srv.Client()}
	err := v.Verify(context.Background(), "stripe", "cs_test_5", store.PurchaseExpectation{OrderID: "order-5", PriceMinor: 499, PriceCurrency: "USD"})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for currency mismatch, got %v", err)
	}
}

func TestStripeVerifier_ExpiredSessionIsInvalid(t *testing.T) {
	srv := fakeStripeServer(t, map[string]map[string]any{
		"cs_test_6": {
			"id": "cs_test_6", "payment_status": "unpaid", "status": "expired",
			"amount_total": int64(499), "currency": "usd", "client_reference_id": "order-6",
		},
	})
	defer srv.Close()

	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: srv.URL, Client: srv.Client()}
	err := v.Verify(context.Background(), "stripe", "cs_test_6", store.PurchaseExpectation{OrderID: "order-6", PriceMinor: 499, PriceCurrency: "USD"})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for an expired session, got %v", err)
	}
}

func TestStripeVerifier_WrongPlatformRejected(t *testing.T) {
	v := &store.StripeVerifier{SecretKey: "sk_test_fake", BaseURL: "http://unused.invalid"}
	err := v.Verify(context.Background(), "google_play", "cs_test_1", store.PurchaseExpectation{OrderID: "order-1", PriceMinor: 499, PriceCurrency: "USD"})
	if err == nil {
		t.Fatal("expected an error when platform is not \"stripe\"")
	}
}
