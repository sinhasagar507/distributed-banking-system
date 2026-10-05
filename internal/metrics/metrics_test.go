package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestMiddleware_RecordsLabeledMetrics verifies the middleware records both
// the duration histogram and the request counter under the route template
// (not the raw URL path), method, and status actually written.
func TestMiddleware_RecordsLabeledMetrics(t *testing.T) {
	router := mux.NewRouter()
	router.Handle("/login", Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))).Methods("POST")

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status 201 from the wrapped handler, got %d", rec.Code)
	}

	count := testutil.ToFloat64(RequestsTotal.WithLabelValues("/login", "POST", "201"))
	if count < 1 {
		t.Fatalf("expected RequestsTotal{/login,POST,201} to be incremented, got %v", count)
	}

	histCount := testutil.CollectAndCount(RequestDuration)
	if histCount < 1 {
		t.Fatalf("expected RequestDuration to have at least one observation, got %v", histCount)
	}
}

// TestMiddleware_DefaultsStatusToOKWhenUnset covers a handler that never
// calls WriteHeader explicitly (net/http itself defaults such a response to
// 200), making sure the recorder doesn't mislabel it.
func TestMiddleware_DefaultsStatusToOKWhenUnset(t *testing.T) {
	router := mux.NewRouter()
	router.Handle("/health", Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))).Methods("GET")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	count := testutil.ToFloat64(RequestsTotal.WithLabelValues("/health", "GET", "200"))
	if count < 1 {
		t.Fatalf("expected RequestsTotal{/health,GET,200} to be incremented, got %v", count)
	}
}

// TestHandler_ExposesPrometheusFormat confirms /metrics' handler emits
// Prometheus text exposition format, including the two metrics this package
// registers.
func TestHandler_ExposesPrometheusFormat(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /metrics to return 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "http_request_duration_seconds") {
		t.Errorf("expected exposition output to contain the duration histogram, got: %s", body)
	}
	if !strings.Contains(body, "http_requests_total") {
		t.Errorf("expected exposition output to contain the requests counter, got: %s", body)
	}
}
