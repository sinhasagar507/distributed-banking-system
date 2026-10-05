// Package logging is DISBank's one structured-logging setup (IMPROVEMENT_PLAN
// §3.1), replacing the ad-hoc fmt.Println/log.Printf calls scattered across
// main.go, db/, and internal/ with a single slog.Logger. A text handler is
// used (not JSON) because the target today is a developer's terminal or
// `docker compose logs`, not a log aggregator; switching to JSON later is a
// one-line change here, not a call-site rewrite.
package logging

import (
	"log/slog"
	"os"
)

// Logger is the process-wide structured logger. It's a package var (like
// db's package-level client) rather than threaded through every function
// signature, since logging is cross-cutting infrastructure, not business
// logic that needs to be mocked in tests.
var Logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
	Level: levelFromEnv(),
}))

// levelFromEnv reads LOG_LEVEL (debug|info|warn|error), defaulting to Info.
func levelFromEnv() slog.Level {
	switch os.Getenv("LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
