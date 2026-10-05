// Package handlers is the thin HTTP transport layer: parse the request, call
// the service layer, encode the response. Business logic lives in
// cse512/internal/service (IMPROVEMENT_PLAN §1.1).
package handlers

import (
	"cse512/internal/service"

	"go.mongodb.org/mongo-driver/mongo"
)

var (
	svc         *service.Service
	mongoClient *mongo.Client
)

// Init wires the shared service instance and Mongo client the handlers call
// into. main.go calls this once at startup, before registering routes. The
// client is only needed by HandleReady (§3.1) — every other handler goes
// through svc.
func Init(s *service.Service, client *mongo.Client) {
	svc = s
	mongoClient = client
}

// Response is the generic {status, message, data} envelope used by /login.
type Response struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}
