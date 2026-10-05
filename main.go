package main

import (
	"context"
	"cse512/db"
	"cse512/handlers"
	"cse512/internal/auth"
	"cse512/internal/config"
	"cse512/internal/httpx"
	"cse512/internal/logging"
	"cse512/internal/metrics"
	"cse512/internal/service"
	"cse512/internal/store"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gorilla/mux"
)

// requestTimeout bounds how long any single request's context stays valid
// (§3.1): a hung downstream Mongo call gets cancelled instead of pinning a
// worker forever. shutdownTimeout bounds how long graceful shutdown waits
// for in-flight requests to finish before giving up.
const (
	requestTimeout  = 10 * time.Second
	shutdownTimeout = 10 * time.Second
)

func main() {
	port := flag.Int("p", 0, "Port to run the server on (falls back to the PORT env var)")
	help := flag.Bool("help", false, "Use p flag to specify port to run the server on")
	flag.Parse()

	if *help {
		flag.PrintDefaults()
		return
	}

	// The -p flag wins; otherwise fall back to PORT from config/env.
	resolvedPort := *port
	if resolvedPort == 0 {
		if p := config.Get().Port; p != "" {
			n, err := strconv.Atoi(p)
			if err != nil {
				logging.Logger.Error("invalid PORT env value", "value", p, "error", err)
				return
			}
			resolvedPort = n
		}
	}

	if resolvedPort == 0 {
		logging.Logger.Error("no port specified; use the -p flag or the PORT env var")
		return
	}

	client := db.GetClient()
	handlers.Init(service.New(store.NewMongoStore(client)), client)

	router := mux.NewRouter()
	// A method-mismatched request never reaches a handler (mux.Methods below
	// already rejects it), but its default response is plain text; route it
	// through the same JSON envelope every other error uses (IMPROVEMENT_PLAN
	// §1.4).
	router.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusMethodNotAllowed, "Invalid request method.")
	})

	// CORS (headers + OPTIONS preflight) wraps every route; auth.Middleware
	// additionally wraps every route except /login, which is what issues the
	// token it requires (IMPROVEMENT_PLAN §1.3/§1.4). metrics.Middleware sits
	// innermost-of-the-three-but-outside-the-handler, registered per-route
	// (not once around the whole router) so mux has already matched the
	// route by the time it runs and can label by path template (§3.2).
	authed := func(methods string, h http.HandlerFunc) http.Handler {
		return httpx.CORS(methods)(auth.Middleware(metrics.Middleware(h)))
	}
	public := func(methods string, h http.HandlerFunc) http.Handler {
		return httpx.CORS(methods)(metrics.Middleware(h))
	}

	router.Handle("/login", public("POST, OPTIONS", handlers.HandleLogin)).Methods("POST", "OPTIONS")
	router.Handle("/transactions", authed("GET, OPTIONS", handlers.HandleTransaction)).Methods("GET", "OPTIONS")
	router.Handle("/transaction", authed("POST, OPTIONS", handlers.PerformTransaction)).Methods("POST", "OPTIONS")
	router.Handle("/monthdata", authed("GET, OPTIONS", handlers.GetMonthData)).Methods("GET", "OPTIONS")

	// /health and /ready (§3.1) aren't wrapped in auth.Middleware — an
	// orchestrator probing liveness/readiness has no token to send — but
	// still get CORS for consistency and so a browser-based status page
	// could poll them directly.
	router.Handle("/health", public("GET, OPTIONS", handlers.HandleHealth)).Methods("GET", "OPTIONS")
	router.Handle("/ready", public("GET, OPTIONS", handlers.HandleReady)).Methods("GET", "OPTIONS")

	// /metrics (§3.2) is scraped by Prometheus, not called by a logged-in
	// user: no auth.Middleware, and left out of metrics.Middleware itself so
	// scrapes don't inflate their own histogram/counter.
	router.Handle("/metrics", httpx.CORS("GET, OPTIONS")(metrics.Handler())).Methods("GET", "OPTIONS")

	// WithRequestID sits outermost (correlates and logs every request, even
	// ones CORS/mux reject before reaching a handler); WithTimeout sits just
	// inside it so the bounded context covers the full handler chain.
	handler := httpx.WithRequestID(httpx.WithTimeout(requestTimeout)(router))

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", resolvedPort),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown (§3.1): on SIGINT/SIGTERM, stop accepting new
	// connections and give in-flight requests up to shutdownTimeout to
	// finish, instead of the process dying mid-request (which, among other
	// things, corrupts in-flight load tests).
	shutdownComplete := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logging.Logger.Info("shutdown signal received, draining in-flight requests", "signal", sig.String())

		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logging.Logger.Error("graceful shutdown did not complete cleanly", "error", err)
		} else {
			logging.Logger.Info("graceful shutdown complete")
		}
		close(shutdownComplete)
	}()

	logging.Logger.Info("starting server", "port", resolvedPort)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logging.Logger.Error("server failed to start", "error", err)
		return
	}

	<-shutdownComplete
}
