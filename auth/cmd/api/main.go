// main is the Lumena API server entrypoint — Phase 2: Authentication.
// Starts a production-ready HTTP server with all /auth/* routes wired.
//
// Configuration via environment variables:
//   PORT           — listen port (default: 8080)
//   JWT_SECRET     — HMAC secret (min 32 bytes; load from secrets manager in prod)
//   JWT_ISSUER     — token issuer claim (default: lumena-api)
//
// Phase 2 uses in-memory stores. Phase 3 adds Postgres + Redis.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lumena/auth/authsvc"
	"github.com/lumena/auth/handlers"
	"github.com/lumena/auth/memrepo"
	"github.com/lumena/auth/otp"
	"github.com/lumena/auth/token"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	port := envOr("PORT", "8080")
	jwtSecret := []byte(envOr("JWT_SECRET", ""))
	jwtIssuer := envOr("JWT_ISSUER", "lumena-api")

	if len(jwtSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be at least 32 bytes (got %d). Load from secrets manager.", len(jwtSecret))
	}

	// Phase 2: in-memory stores. Phase 3: swap to Postgres + Redis.
	repo := memrepo.New()
	otpStore := otp.NewMemoryStore()
	otpSender := newOTPSender()
	otpSvc := otp.NewService(otpStore, otpSender)
	issuer, err := token.NewIssuer(jwtSecret, jwtIssuer, 15*time.Minute)
	if err != nil {
		return fmt.Errorf("create token issuer: %w", err)
	}
	svc := authsvc.NewService(repo, otpSvc, issuer)
	h := handlers.New(svc, issuer)

	mux := http.NewServeMux()
	registerRoutes(mux, h)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      loggingMiddleware(logger, corsMiddleware(mux)),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server starting", "port", port, "phase", "2-authentication")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-stop:
		logger.Info("shutting down gracefully")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	}
}

func registerRoutes(mux *http.ServeMux, h *handlers.Handler) {
	// Public auth routes
	mux.HandleFunc("POST /auth/phone/start", h.PhoneStart)
	mux.HandleFunc("POST /auth/phone/verify", h.PhoneVerify)
	mux.HandleFunc("POST /auth/email/register", h.EmailRegister)
	mux.HandleFunc("POST /auth/email/login", h.EmailLogin)
	mux.HandleFunc("POST /auth/refresh", h.Refresh)
	mux.HandleFunc("POST /auth/logout", h.Logout)

	// Authenticated routes
	authed := func(pattern string, handlerFn http.HandlerFunc) {
		mux.Handle(pattern, h.AuthMiddleware(handlerFn))
	}
	authed("POST /auth/age/declare", h.DeclareAge)
	authed("GET /auth/age/status", h.AgeStatus)
	authed("POST /me/delete", h.RequestDeletion)
	authed("POST /me/delete/cancel", h.CancelDeletion)

	// Health check
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","phase":"2-authentication"}`))
	})

	// NOT IMPLEMENTED stubs — clearly marked, return 501
	notImplemented := func(pattern string) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"error":{"code":"NOT_IMPLEMENTED","message":"This endpoint is not yet implemented. See implementation roadmap Phase 3+."}}`))
		})
	}
	notImplemented("POST /auth/oauth/{provider}")
	notImplemented("GET /auth/sessions")
	notImplemented("DELETE /auth/sessions/{id}")
	notImplemented("POST /auth/age/assure")
	notImplemented("POST /me")
	notImplemented("GET /me")
	notImplemented("GET /feed")
	notImplemented("GET /rooms/{id}")
	notImplemented("POST /gifts/send")
	notImplemented("GET /wallet")
}

// loggingMiddleware logs every request with method, path, status, and latency.
func loggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency_ms", time.Since(start).Milliseconds(),
			"ip", r.RemoteAddr,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// corsMiddleware adds CORS headers for the web client.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", envOr("CORS_ORIGIN", "http://localhost:3000"))
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key, X-Region-Code")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newOTPSender returns the appropriate sender based on environment.
func newOTPSender() otp.Sender {
	env := envOr("LUMENA_ENV", "development")
	if env == "production" {
		// NOT IMPLEMENTED — replace with SMS vendor (Twilio/AWS SNS) in production
		// Using LogSender in non-dev environments will expose codes in logs — replace before launch
		slog.Warn("OTP SENDER: production SMS vendor NOT IMPLEMENTED — using LogSender")
	}
	return &otp.LogSender{} // DEV MOCK
}
