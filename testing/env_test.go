package main

import "os"

// baseURL returns the backend origin these integration tests hit, read from
// API_BASE_URL (so the same tests run against a local instance, the
// docker-compose stack, or a CI service) with the long-standing local-dev
// default (IMPROVEMENT_PLAN §2.3).
func baseURL() string {
	if v := os.Getenv("API_BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}
