package store_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lumena/ledger/store"
)

// fakeServiceAccountJSON generates a throwaway RSA key and wraps it in the
// same JSON shape Google's downloaded service-account key files use, so
// GooglePlayVerifier's real PEM-parsing and JWT-signing code runs exactly
// as it would against a genuine key — it's just never checked against
// Google's real servers, matching the whole package's "real call to
// something we can't reach in dev" discipline.
func fakeServiceAccountJSON(t *testing.T, tokenURL string) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	sa := map[string]string{
		"client_email": "fake@fake-project.iam.gserviceaccount.com",
		"private_key":  string(pemKey),
		"token_uri":    tokenURL,
	}
	b, err := json.Marshal(sa)
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	return b
}

// fakeOAuthServer stands in for https://oauth2.googleapis.com/token —
// it doesn't validate the JWT assertion's signature (that would just be
// re-testing Google's own verifier), it confirms the request shape
// GooglePlayVerifier sends is correct and hands back a fixed token.
func fakeOAuthServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || r.Form.Get("assertion") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fake-access-token", "expires_in": 3600})
	}))
}

// fakeAndroidPublisherServer stands in for androidpublisher.googleapis.com.
func fakeAndroidPublisherServer(t *testing.T, purchases map[string]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// The purchase token is the last path segment.
		path := r.URL.Path
		id := path[lastIndex(path, '/')+1:]
		p, ok := purchases[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(p)
	}))
}

func lastIndex(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func newTestVerifier(t *testing.T, oauthURL, publisherURL string) *store.GooglePlayVerifier {
	t.Helper()
	saJSON := fakeServiceAccountJSON(t, oauthURL)
	v, err := store.NewGooglePlayVerifier(saJSON, "com.lumenalive.app")
	if err != nil {
		t.Fatalf("NewGooglePlayVerifier: %v", err)
	}
	v.AndroidPublisherBaseURL = publisherURL
	return v
}

func TestGooglePlayVerifier_ValidPurchaseMatchingSKUSucceeds(t *testing.T) {
	oauth := fakeOAuthServer(t)
	defer oauth.Close()
	publisher := fakeAndroidPublisherServer(t, map[string]map[string]any{
		"token-1": {"purchaseState": 0, "productId": "coins_100", "orderId": "GPA.1", "acknowledgementState": 0},
	})
	defer publisher.Close()

	v := newTestVerifier(t, oauth.URL, publisher.URL)
	err := v.Verify(context.Background(), "google_play", "token-1", store.PurchaseExpectation{
		OrderID: "order-1", SKU: "coins_100",
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestGooglePlayVerifier_ProductIDMismatchRejected(t *testing.T) {
	// A genuinely purchased token — but for a different (cheaper) product
	// than this order expects. Without this check a client could buy
	// coins_100 and replay the token against an order for coins_2000.
	oauth := fakeOAuthServer(t)
	defer oauth.Close()
	publisher := fakeAndroidPublisherServer(t, map[string]map[string]any{
		"token-2": {"purchaseState": 0, "productId": "coins_100", "orderId": "GPA.2"},
	})
	defer publisher.Close()

	v := newTestVerifier(t, oauth.URL, publisher.URL)
	err := v.Verify(context.Background(), "google_play", "token-2", store.PurchaseExpectation{
		OrderID: "order-2", SKU: "coins_2000",
	})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for SKU mismatch, got %v", err)
	}
}

func TestGooglePlayVerifier_PendingPurchaseStateIsPending(t *testing.T) {
	oauth := fakeOAuthServer(t)
	defer oauth.Close()
	publisher := fakeAndroidPublisherServer(t, map[string]map[string]any{
		"token-3": {"purchaseState": 2, "productId": "coins_100", "orderId": "GPA.3"},
	})
	defer publisher.Close()

	v := newTestVerifier(t, oauth.URL, publisher.URL)
	err := v.Verify(context.Background(), "google_play", "token-3", store.PurchaseExpectation{
		OrderID: "order-3", SKU: "coins_100",
	})
	if err != store.ErrPending {
		t.Fatalf("expected ErrPending, got %v", err)
	}
}

func TestGooglePlayVerifier_CanceledPurchaseIsInvalid(t *testing.T) {
	oauth := fakeOAuthServer(t)
	defer oauth.Close()
	publisher := fakeAndroidPublisherServer(t, map[string]map[string]any{
		"token-4": {"purchaseState": 1, "productId": "coins_100", "orderId": "GPA.4"},
	})
	defer publisher.Close()

	v := newTestVerifier(t, oauth.URL, publisher.URL)
	err := v.Verify(context.Background(), "google_play", "token-4", store.PurchaseExpectation{
		OrderID: "order-4", SKU: "coins_100",
	})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for a canceled purchase, got %v", err)
	}
}

func TestGooglePlayVerifier_UnknownTokenIsInvalid(t *testing.T) {
	oauth := fakeOAuthServer(t)
	defer oauth.Close()
	publisher := fakeAndroidPublisherServer(t, map[string]map[string]any{})
	defer publisher.Close()

	v := newTestVerifier(t, oauth.URL, publisher.URL)
	err := v.Verify(context.Background(), "google_play", "does-not-exist", store.PurchaseExpectation{
		OrderID: "order-5", SKU: "coins_100",
	})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestGooglePlayVerifier_ObfuscatedAccountIDMismatchRejected(t *testing.T) {
	// The classic token-replay-to-a-different-account attack: a genuinely
	// purchased token for the right SKU, but purchased under a different
	// account's obfuscatedAccountId than this order belongs to.
	oauth := fakeOAuthServer(t)
	defer oauth.Close()
	publisher := fakeAndroidPublisherServer(t, map[string]map[string]any{
		"token-6": {"purchaseState": 0, "productId": "coins_100", "orderId": "GPA.6", "obfuscatedExternalAccountId": "acc-mallory"},
	})
	defer publisher.Close()

	v := newTestVerifier(t, oauth.URL, publisher.URL)
	err := v.Verify(context.Background(), "google_play", "token-6", store.PurchaseExpectation{
		OrderID: "order-6", SKU: "coins_100", AccountID: "acc-alice",
	})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken for account id mismatch, got %v", err)
	}
}

func TestGooglePlayVerifier_WrongPlatformRejected(t *testing.T) {
	v := &store.GooglePlayVerifier{PackageName: "com.lumenalive.app", AndroidPublisherBaseURL: "http://unused.invalid"}
	err := v.Verify(context.Background(), "stripe", "token-1", store.PurchaseExpectation{OrderID: "order-1", SKU: "coins_100"})
	if err == nil {
		t.Fatal("expected an error when platform is not \"google_play\"")
	}
}

func TestGooglePlayVerifier_EmptySKURejected(t *testing.T) {
	v := &store.GooglePlayVerifier{PackageName: "com.lumenalive.app"}
	err := v.Verify(context.Background(), "google_play", "token-1", store.PurchaseExpectation{OrderID: "order-1"})
	if err != store.ErrInvalidToken {
		t.Fatalf("expected ErrInvalidToken when SKU is missing, got %v", err)
	}
}
