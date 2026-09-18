// Package idempotency implements server-side idempotency for mutating endpoints.
// Required on: POST /gifts/send, POST /orders, POST /orders/{id}/verify,
//              POST /private-video/requests, POST /payouts.
// Retention: 24 hours.
// Replay: returns the original response body with Idempotent-Replay: true header.
package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

const (
	HeaderKey         = "Idempotency-Key"
	HeaderReplay      = "Idempotent-Replay"
	keyMaxLen         = 128
	retention         = 24 * time.Hour
)

// Store is the persistence interface for idempotency records.
// In production this is backed by Redis with a 24h TTL.
type Store interface {
	// Get returns (responseBody, statusCode, true) if a record exists, or (nil, 0, false).
	Get(ctx context.Context, key string) (body []byte, status int, found bool, err error)
	// Set records a response. Ignores duplicate sets (the first wins).
	Set(ctx context.Context, key string, body []byte, status int, ttl time.Duration) error
}

// Middleware wraps handlers that require idempotency.
// If the Idempotency-Key header is absent, it returns 400.
// If a prior response is found, it replays it with Idempotent-Replay: true.
func Middleware(store Store, required bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(HeaderKey)
			if key == "" {
				if required {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"error": map[string]any{
							"code":    "MISSING_IDEMPOTENCY_KEY",
							"message": "The Idempotency-Key header is required for this request.",
						},
					})
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if len(key) > keyMaxLen {
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			body, status, found, err := store.Get(r.Context(), key)
			if err == nil && found {
				w.Header().Set(HeaderReplay, "true")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(body)
				return
			}

			// Capture the response so we can store it.
			rec := &responseRecorder{ResponseWriter: w, buf: &bytes.Buffer{}, status: 200}
			next.ServeHTTP(rec, r)

			// Only store successful responses (2xx).
			if rec.status >= 200 && rec.status < 300 {
				_ = store.Set(r.Context(), key, rec.buf.Bytes(), rec.status, retention)
			}
		})
	}
}

// responseRecorder captures the response for storage.
type responseRecorder struct {
	http.ResponseWriter
	buf    *bytes.Buffer
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}
