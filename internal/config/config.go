// Package config centralizes all externally-tunable deployment settings.
//
// Every knob is read from the environment with a default that preserves the
// original local-dev behavior, so an existing `go run main.go -p 8080` is
// unaffected while the same binary can be retargeted (e.g. service-name Mongo
// URIs inside Docker) without a recompile.
package config

import (
	"os"
	"strconv"
	"sync"

	"cse512/internal/logging"
)

// Config holds the process-wide configuration loaded from the environment.
type Config struct {
	MongoURI      string // MONGO_URI
	MongoMaxPool  uint64 // MONGO_MAX_POOL
	MongoMinPool  uint64 // MONGO_MIN_POOL
	MongoReadPref string // MONGO_READ_PREF
	Port          string // PORT (empty ⇒ must be supplied via the -p flag)
	JWTSecret     string // JWT_SECRET
}

// Defaults reproduce today's hardcoded local behavior.
const (
	defaultMongoURI      = "mongodb://localhost:27151,localhost:27152,localhost:27153"
	defaultMongoMaxPool  = 30000
	defaultMongoMinPool  = 10
	defaultMongoReadPref = "secondary"
	defaultJWTSecret     = "dev-insecure-secret-change-me"
)

var (
	cfg  *Config
	once sync.Once
)

// Get returns the process-wide configuration, loading it from the environment
// exactly once.
func Get() *Config {
	once.Do(func() { cfg = load() })
	return cfg
}

func load() *Config {
	return &Config{
		MongoURI:      getEnv("MONGO_URI", defaultMongoURI),
		MongoMaxPool:  getEnvUint("MONGO_MAX_POOL", defaultMongoMaxPool),
		MongoMinPool:  getEnvUint("MONGO_MIN_POOL", defaultMongoMinPool),
		MongoReadPref: getEnv("MONGO_READ_PREF", defaultMongoReadPref),
		Port:          getEnv("PORT", ""),
		JWTSecret:     getEnv("JWT_SECRET", defaultJWTSecret),
	}
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func getEnvUint(key string, fallback uint64) uint64 {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return fallback
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		logging.Logger.Warn("config: invalid value, using default", "key", key, "value", v, "default", fallback, "error", err)
		return fallback
	}
	return n
}
