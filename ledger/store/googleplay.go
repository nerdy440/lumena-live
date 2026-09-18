package store

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GooglePlayServiceAccount is the subset of a Google Cloud service-account
// key JSON (Play Console → Setup → API access → service account →
// "Manage keys" → create key) needed to authenticate server-to-server
// against the Play Developer API.
type GooglePlayServiceAccount struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"` // usually https://oauth2.googleapis.com/token
}

// GooglePlayVerifier verifies Android in-app purchases against the Google
// Play Developer API's purchases.products.get endpoint — the store Google
// requires for any digital good "consumed within the app" on apps
// distributed via Play Store (coins/diamonds fall squarely under this;
// using Stripe for that specific flow is a policy violation even though
// Stripe is fine for creator payouts).
//
// Same discipline as StripeVerifier (doc rule 8): this makes a real,
// authenticated HTTPS call and validates a real response shape. It is
// untested against Google's live servers only because no service account
// exists in this dev environment — not because any part of it is mocked.
//
// PackageName must match the Android app's applicationId, and each
// ledger.Product's SKU must be configured as an identically-named
// in-app product in the Play Console. Verify's primary anti-replay check
// is the purchased product ID against the order's SKU — unlike Stripe
// Checkout, Play Billing enforces the price itself inside Google's own
// payment sheet, so there's no client-tamperable amount to double-check
// here; the meaningful cross-check is "was this token issued for the SKU
// this order actually created," matching PurchaseExpectation.SKU, plus
// the obfuscatedExternalAccountId set at purchase time matching
// PurchaseExpectation.AccountID when the client provided one.
type GooglePlayVerifier struct {
	ServiceAccount          GooglePlayServiceAccount
	PackageName             string
	AndroidPublisherBaseURL string // defaults to https://androidpublisher.googleapis.com
	TokenURL                string // defaults to ServiceAccount.TokenURI, else https://oauth2.googleapis.com/token
	Client                  *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// NewGooglePlayVerifier parses serviceAccountJSON (the raw bytes of a
// downloaded Play Console service-account key file) and returns a ready
// Verifier for packageName (the Android app's applicationId).
func NewGooglePlayVerifier(serviceAccountJSON []byte, packageName string) (*GooglePlayVerifier, error) {
	var sa GooglePlayServiceAccount
	if err := json.Unmarshal(serviceAccountJSON, &sa); err != nil {
		return nil, fmt.Errorf("store: parse google play service account json: %w", err)
	}
	if sa.ClientEmail == "" || sa.PrivateKey == "" {
		return nil, errors.New("store: google play service account json missing client_email or private_key")
	}
	tokenURL := sa.TokenURI
	if tokenURL == "" {
		tokenURL = "https://oauth2.googleapis.com/token"
	}
	return &GooglePlayVerifier{
		ServiceAccount:          sa,
		PackageName:             packageName,
		AndroidPublisherBaseURL: "https://androidpublisher.googleapis.com",
		TokenURL:                tokenURL,
		Client:                  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

var _ Verifier = (*GooglePlayVerifier)(nil)

// productPurchase is the subset of the Play Developer API's
// ProductPurchase resource this verifier checks.
// https://developers.google.com/android-publisher/api-ref/rest/v3/purchases.products
type productPurchase struct {
	// PurchaseState: 0 = purchased, 1 = canceled, 2 = pending.
	PurchaseState               int64  `json:"purchaseState"`
	ProductID                   string `json:"productId"`
	OrderID                     string `json:"orderId"`
	ObfuscatedExternalAccountId string `json:"obfuscatedExternalAccountId"`
	AcknowledgementState        int64  `json:"acknowledgementState"`
}

func (v *GooglePlayVerifier) Verify(ctx context.Context, platform, purchaseToken string, expected PurchaseExpectation) error {
	if platform != "google_play" {
		return fmt.Errorf("store: GooglePlayVerifier cannot verify platform %q", platform)
	}
	if purchaseToken == "" || expected.SKU == "" {
		return ErrInvalidToken
	}

	accessToken, err := v.getAccessToken(ctx)
	if err != nil {
		// A token-endpoint hiccup (network blip, Google outage) is not
		// proof the purchase itself is invalid — let the reconciler retry.
		return ErrPending
	}

	reqURL := fmt.Sprintf("%s/androidpublisher/v3/applications/%s/purchases/products/%s/tokens/%s",
		strings.TrimRight(v.AndroidPublisherBaseURL, "/"),
		url.PathEscape(v.PackageName), url.PathEscape(expected.SKU), url.PathEscape(purchaseToken))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := v.Client.Do(req)
	if err != nil {
		return ErrPending
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrInvalidToken
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("store: android publisher returned %d", resp.StatusCode)
	}

	var pp productPurchase
	if err := json.NewDecoder(resp.Body).Decode(&pp); err != nil {
		return fmt.Errorf("store: could not parse android publisher response: %w", err)
	}

	switch pp.PurchaseState {
	case 2:
		return ErrPending
	case 0:
		// purchased — fall through to the cross-checks below
	default:
		return ErrInvalidToken
	}

	if pp.ProductID != expected.SKU {
		return ErrInvalidToken
	}
	if expected.AccountID != "" && pp.ObfuscatedExternalAccountId != "" && pp.ObfuscatedExternalAccountId != expected.AccountID {
		return ErrInvalidToken
	}

	return nil
}

// getAccessToken returns a cached OAuth2 access token, refreshing it via
// the service account's JWT bearer flow once it's within a minute of
// expiring.
func (v *GooglePlayVerifier) getAccessToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.accessToken != "" && time.Now().Before(v.expiresAt.Add(-1*time.Minute)) {
		return v.accessToken, nil
	}

	assertion, err := v.signJWT()
	if err != nil {
		return "", err
	}

	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("store: google oauth token endpoint returned %d", resp.StatusCode)
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("store: could not parse google oauth token response: %w", err)
	}

	v.accessToken = tok.AccessToken
	v.expiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	return v.accessToken, nil
}

// signJWT builds and RS256-signs the JWT assertion Google's OAuth2 token
// endpoint requires for the service-account JWT bearer flow:
// https://developers.google.com/identity/protocols/oauth2/service-account#jwt-auth
func (v *GooglePlayVerifier) signJWT() (string, error) {
	key, err := parseRSAPrivateKeyPEM(v.ServiceAccount.PrivateKey)
	if err != nil {
		return "", err
	}

	now := time.Now()
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iss":   v.ServiceAccount.ClientEmail,
		"scope": "https://www.googleapis.com/auth/androidpublisher",
		"aud":   v.TokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("store: sign jwt: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseRSAPrivateKeyPEM accepts either PKCS#1 or PKCS#8 PEM encoding —
// Google's downloaded service-account keys use PKCS#8.
func parseRSAPrivateKeyPEM(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("store: could not decode PEM private key")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	keyIfc, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("store: parse private key: %w", err)
	}
	key, ok := keyIfc.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("store: private key is not RSA")
	}
	return key, nil
}
