// Package httpx is the shared HTTP plumbing every handler used to hand-write
// for itself (IMPROVEMENT_PLAN §1.4): the four CORS headers, the OPTIONS
// preflight short-circuit, and a single JSON error envelope. Before this, the
// same code was copy-pasted into all four handlers and had already drifted
// (each one set a slightly different Access-Control-Allow-Methods).
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"cse512/internal/logging"
)

// newRequestID returns a short random hex ID. crypto/rand over a UUID
// library: request IDs just need to be unique enough to grep a correlated
// pair of log lines, not globally unique or RFC-4122 shaped, so this avoids
// adding a dependency for it.
func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type contextKey int

const requestIDKey contextKey = iota

// RequestID returns the request ID a request's context carries, or "" if
// WithRequestID never ran for it.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// WithRequestID (§3.1) assigns every request a short correlation ID, logs its
// start/end via slog, and stashes the ID in the request context so handlers
// and the service/store layers can tag their own log lines with it. It sits
// outermost (wraps CORS) so even a request CORS rejects still gets logged.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		r = r.WithContext(ctx)

		start := time.Now()
		logging.Logger.Info("request started", "request_id", id, "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
		logging.Logger.Info("request completed", "request_id", id, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}

// WithTimeout (§3.1) bounds every request's context to d, so a hung
// downstream call (e.g. Mongo) can't block a worker forever. Handlers that
// respect ctx cancellation (every Mongo driver call does) return promptly
// once it fires; this doesn't itself write a response on timeout, since
// net/http already returns on the client side once the connection's own
// WriteTimeout elapses.
func WithTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ErrorEnvelope is the one JSON shape every endpoint's *error* responses use
// (success responses stay endpoint-specific: a transaction result object, an
// array of transaction summaries, a CSV file, login data). Before this,
// errors were a mix of ad-hoc structs and, in monthdata.go, plain text via
// http.Error — inconsistent shapes the frontend had to special-case.
type ErrorEnvelope struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// Error writes status and message as the standard JSON error envelope.
func Error(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorEnvelope{Status: "error", Message: message})
}

// CORS sets the standard Access-Control-Allow-* headers (origin "*", the
// given methods, Content-Type-only headers) and answers an OPTIONS preflight
// with a bare 200, before handing off to next. methods is the exact
// Access-Control-Allow-Methods value for that route (e.g. "GET, OPTIONS"),
// kept per-route (not a single global value) so this doesn't trade one kind
// of drift for another — every route still advertises only what it accepts.
func CORS(methods string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", methods)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Content-Type", "application/json")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusOK)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
