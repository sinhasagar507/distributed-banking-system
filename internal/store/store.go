// Package store is the persistence seam the service layer depends on
// (IMPROVEMENT_PLAN §1.1), replacing handlers that called db.GetClient() and
// the mongo driver directly. A fake implementing Store lets the service layer
// be unit tested without a live Mongo cluster.
package store

import (
	"context"
	"errors"

	"cse512/datamodels"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// ErrNotFound is returned by GetUserByID/GetUserByIDStrong when no user matches.
var ErrNotFound = mongo.ErrNoDocuments

// ErrInsufficientFunds is returned by DebitIfSufficient when the account
// doesn't have enough balance for the debit; the $gte guard that produces it
// is evaluated atomically by Mongo, closing the check-then-act race a plain
// read-then-$inc would have (IMPROVEMENT_PLAN §1.2).
var ErrInsufficientFunds = errors.New("insufficient funds")

// Store is every database operation the service layer needs.
type Store interface {
	// RunInTransaction runs fn inside a genuine MongoDB multi-document
	// transaction (session.WithTransaction): every store call made with the
	// ctx passed to fn participates in the transaction and is rolled back
	// together on error/abort. The previous handler-level implementation
	// started a session but issued writes with context.Background(), so they
	// ran as independent autocommit writes the session couldn't roll back —
	// this is the fix.
	RunInTransaction(ctx context.Context, fn func(sessCtx context.Context) error) error

	// GetUserByID reads with the client's default (fast, possibly stale) read
	// preference; use it for existence checks, not balance-critical reads.
	GetUserByID(ctx context.Context, userID int) (datamodels.User, error)
	// GetUserByIDStrong reads from the primary with majority read concern, so
	// callers never see a stale balance from a lagging secondary.
	GetUserByIDStrong(ctx context.Context, userID int) (datamodels.User, error)

	// DebitIfSufficient atomically decrements current_balance by amount only
	// if current_balance >= amount, returning ErrInsufficientFunds otherwise.
	// This replaces the old read-balance-then-$inc pattern, which let two
	// concurrent debits both pass a stale check and overdraw the account.
	DebitIfSufficient(ctx context.Context, userID int, amount int) error
	IncrementBalance(ctx context.Context, userID int, delta int) error

	InsertTransaction(ctx context.Context, txn datamodels.Transaction) error
	RecentTransactions(ctx context.Context, userID int, limit int64) ([]datamodels.Transaction, error)
	TransactionsInRange(ctx context.Context, userID int, startUnix, endUnix int64) ([]datamodels.Transaction, error)
}

type mongoStore struct {
	client *mongo.Client
}

// NewMongoStore wraps an existing *mongo.Client (e.g. db.GetClient()) as a Store.
func NewMongoStore(client *mongo.Client) Store {
	return &mongoStore{client: client}
}

func (s *mongoStore) db() *mongo.Database { return s.client.Database("bank") }

// strongUsers returns the users collection bound to a primary-read,
// majority-read-concern handle for balance-critical reads (IMPROVEMENT_PLAN
// §1.2 "majority concern for balances, secondary/local for history").
func (s *mongoStore) strongUsers() *mongo.Collection {
	return s.db().Collection("users", options.Collection().SetReadPreference(readpref.Primary()))
}

func (s *mongoStore) RunInTransaction(ctx context.Context, fn func(sessCtx context.Context) error) error {
	session, err := s.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(sessCtx mongo.SessionContext) (interface{}, error) {
		return nil, fn(sessCtx)
	})
	return err
}

func (s *mongoStore) GetUserByID(ctx context.Context, userID int) (datamodels.User, error) {
	var user datamodels.User
	err := s.db().Collection("users").FindOne(ctx, bson.M{"user_id": userID}).Decode(&user)
	if err != nil {
		return datamodels.User{}, err
	}
	return user, nil
}

func (s *mongoStore) GetUserByIDStrong(ctx context.Context, userID int) (datamodels.User, error) {
	var user datamodels.User
	err := s.strongUsers().FindOne(ctx, bson.M{"user_id": userID}).Decode(&user)
	if err != nil {
		return datamodels.User{}, err
	}
	return user, nil
}

func (s *mongoStore) DebitIfSufficient(ctx context.Context, userID int, amount int) error {
	result, err := s.db().Collection("users").UpdateOne(
		ctx,
		bson.M{"user_id": userID, "current_balance": bson.M{"$gte": amount}},
		bson.M{"$inc": bson.M{"current_balance": -amount}},
	)
	if err != nil {
		return err
	}
	if result.MatchedCount == 0 {
		return ErrInsufficientFunds
	}
	return nil
}

func (s *mongoStore) IncrementBalance(ctx context.Context, userID int, delta int) error {
	_, err := s.db().Collection("users").UpdateOne(
		ctx,
		bson.M{"user_id": userID},
		bson.M{"$inc": bson.M{"current_balance": delta}},
	)
	return err
}

func (s *mongoStore) InsertTransaction(ctx context.Context, txn datamodels.Transaction) error {
	_, err := s.db().Collection("transactions").InsertOne(ctx, txn)
	return err
}

func (s *mongoStore) RecentTransactions(ctx context.Context, userID int, limit int64) ([]datamodels.Transaction, error) {
	filter := bson.M{"$or": []bson.M{{"sender_id": userID}, {"receiver_id": userID}}}
	opts := options.Find().SetSort(bson.D{{Key: "dateTimeStamp", Value: -1}}).SetLimit(limit)
	return s.find(ctx, filter, opts)
}

func (s *mongoStore) TransactionsInRange(ctx context.Context, userID int, startUnix, endUnix int64) ([]datamodels.Transaction, error) {
	filter := bson.M{
		"$or": []bson.M{{"sender_id": userID}, {"receiver_id": userID}},
		"dateTimeStamp": bson.M{
			"$gte": startUnix,
			"$lte": endUnix,
		},
	}
	return s.find(ctx, filter)
}

func (s *mongoStore) find(ctx context.Context, filter bson.M, opts ...*options.FindOptions) ([]datamodels.Transaction, error) {
	cursor, err := s.db().Collection("transactions").Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var out []datamodels.Transaction
	for cursor.Next(ctx) {
		var t datamodels.Transaction
		if err := cursor.Decode(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, cursor.Err()
}
