// Package metrics is DISBank's Prometheus instrumentation (IMPROVEMENT_PLAN
// §3.2): one latency histogram and one request counter, both labeled by
// route/method/status, wired in as an httpx middleware so every handler gets
// them for free instead of each one instrumenting itself ad hoc.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// RequestDuration is a histogram, not a gauge/average, because the project's
// whole thesis is tail latency under scale (p95/p99) — an average throws
// exactly the signal away that distinguishes a scaled system from an
// unscaled one.
var RequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds, labeled by route/method/status.",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"route", "method", "status"},
)

// RequestsTotal counts every request (2xx included), labeled by status, so
// "Rate(status=~5xx) over Rate(total)" gives the error rate without a
// second, separately-maintained error-only counter drifting from it.
var RequestsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests, labeled by route/method/status.",
	},
	[]string{"route", "method", "status"},
)

func init() {
	prometheus.MustRegister(RequestDuration, RequestsTotal)
}

// statusRecorder captures the status code a handler writes, since
// http.ResponseWriter doesn't expose it after the fact.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Middleware records RequestDuration/RequestsTotal for every request. It
// reads mux's own route-match (CurrentRoute + GetPathTemplate) for the
// "route" label instead of r.URL.Path, so /health and /login aren't counted
// as different cardinality buckets per random query string. It must sit
// inside the router (registered per-route, see main.go) so mux has already
// matched the route by the time it runs.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		route := r.URL.Path
		if tmpl, err := mux.CurrentRoute(r).GetPathTemplate(); err == nil {
			route = tmpl
		}
		status := strconv.Itoa(rec.status)

		RequestDuration.WithLabelValues(route, r.Method, status).Observe(time.Since(start).Seconds())
		RequestsTotal.WithLabelValues(route, r.Method, status).Inc()
	})
}

// Handler is the /metrics endpoint itself: Prometheus' own default registry
// exposition handler, unauthenticated (it's scraped by Prometheus, not a
// logged-in user — see main.go, where it's registered outside
// auth.Middleware).
func Handler() http.Handler {
	return promhttp.Handler()
}
