package db

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"cse512/internal/config"
	"cse512/internal/logging"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	client     *mongo.Client
	clientOnce sync.Once
)

func connect() {
	cfg := config.Get()

	// Configure connection pool settings from externalized config.
	clientOptions := options.Client().ApplyURI(cfg.MongoURI).
		SetMaxPoolSize(cfg.MongoMaxPool).
		SetMinPoolSize(cfg.MongoMinPool).
		SetMaxConnIdleTime(5 * time.Minute).
		SetReadPreference(readPreference(cfg.MongoReadPref)).
		SetReadConcern(readconcern.Local())

	// Establish connection to the MongoDB server
	temp, err := mongo.Connect(context.Background(), clientOptions)
	if err != nil {
		logging.Logger.Error("failed to connect to MongoDB", "error", err)
		os.Exit(1)
	}

	// Ping to check connection
	if err = temp.Ping(context.Background(), nil); err != nil {
		logging.Logger.Error("failed to ping MongoDB", "error", err)
		os.Exit(1)
	}

	client = temp
}

// GetClient returns the shared MongoDB client, dialing exactly once. The
// sync.Once guarantees concurrent first-callers can't race into a double-dial.
func GetClient() *mongo.Client {
	clientOnce.Do(connect)
	return client
}

// readPreference maps a config string to a mongo read preference, defaulting
// to Secondary (the original behavior) for empty or unrecognized values.
func readPreference(pref string) *readpref.ReadPref {
	switch strings.ToLower(strings.TrimSpace(pref)) {
	case "primary":
		return readpref.Primary()
	case "primarypreferred":
		return readpref.PrimaryPreferred()
	case "secondary":
		return readpref.Secondary()
	case "secondarypreferred":
		return readpref.SecondaryPreferred()
	case "nearest":
		return readpref.Nearest()
	default:
		logging.Logger.Warn("config: unknown MONGO_READ_PREF, defaulting to secondary", "value", pref)
		return readpref.Secondary()
	}
}
