package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// HandleHealth is the liveness probe (IMPROVEMENT_PLAN §3.1): the process is
// up and able to handle HTTP at all. It deliberately checks nothing
// downstream, so a transient Mongo blip doesn't get an orchestrator to kill
// and restart an otherwise-healthy instance — that's what HandleReady is for.
func HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// HandleReady is the readiness probe (IMPROVEMENT_PLAN §3.1): pings Mongo
// with a short, bounded timeout so a load balancer or compose/orchestrator
// healthcheck can stop routing traffic to an instance that's up but can't
// actually reach its database.
func HandleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	w.Header().Set("Content-Type", "application/json")
	if err := mongoClient.Ping(ctx, nil); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "unavailable", "message": err.Error()})
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
