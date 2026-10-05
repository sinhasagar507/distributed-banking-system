// Package storetest is a fake store.Store for unit-testing internal/service
// without a live MongoDB cluster (IMPROVEMENT_PLAN §2.1). It's a regular
// (non-_test.go) file, not inside package store itself, so it can be
// imported from service's test files while keeping test-only code out of the
// production store package.
//
// It's a plain in-memory map with no real transaction isolation — testing
// Mongo's actual transaction/concurrency semantics is explicitly out of
// scope here (that's the integration/concurrency job, §2.2/2.3). What it
// does give tests is control over error injection, so the service layer's
// branching logic (which message for which failure, which branch logs a
// failed transaction) can be exercised deterministically.
package storetest

import (
	"context"
	"sort"
	"sync"

	"cse512/datamodels"
	"cse512/internal/store"
)

// FakeStore implements store.Store over an in-memory map.
type FakeStore struct {
	mu sync.Mutex

	Users        map[int]datamodels.User
	Transactions []datamodels.Transaction

	// Error injection, checked before the normal in-memory logic. Keyed by
	// user_id where relevant; a nil map or missing key means "don't inject."
	GetUserErr           map[int]error
	DebitErr             map[int]error
	IncrementErr         map[int]error
	InsertTransactionErr error
	RunInTransactionErr  error // if set, RunInTransaction returns this without calling fn
}

// New returns an empty FakeStore. Use Users[id] = datamodels.User{...} to seed accounts.
func New() *FakeStore {
	return &FakeStore{Users: map[int]datamodels.User{}}
}

var _ store.Store = (*FakeStore)(nil)

func (f *FakeStore) RunInTransaction(ctx context.Context, fn func(sessCtx context.Context) error) error {
	if f.RunInTransactionErr != nil {
		return f.RunInTransactionErr
	}
	return fn(ctx)
}

func (f *FakeStore) GetUserByID(ctx context.Context, userID int) (datamodels.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.GetUserErr[userID]; ok {
		return datamodels.User{}, err
	}
	u, ok := f.Users[userID]
	if !ok {
		return datamodels.User{}, store.ErrNotFound
	}
	return u, nil
}

func (f *FakeStore) GetUserByIDStrong(ctx context.Context, userID int) (datamodels.User, error) {
	return f.GetUserByID(ctx, userID)
}

func (f *FakeStore) DebitIfSufficient(ctx context.Context, userID int, amount int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.DebitErr[userID]; ok {
		return err
	}
	u, ok := f.Users[userID]
	if !ok {
		return store.ErrNotFound
	}
	if u.Balance < amount {
		return store.ErrInsufficientFunds
	}
	u.Balance -= amount
	f.Users[userID] = u
	return nil
}

func (f *FakeStore) IncrementBalance(ctx context.Context, userID int, delta int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.IncrementErr[userID]; ok {
		return err
	}
	u, ok := f.Users[userID]
	if !ok {
		return store.ErrNotFound
	}
	u.Balance += delta
	f.Users[userID] = u
	return nil
}

func (f *FakeStore) InsertTransaction(ctx context.Context, txn datamodels.Transaction) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.InsertTransactionErr != nil {
		return f.InsertTransactionErr
	}
	f.Transactions = append(f.Transactions, txn)
	return nil
}

func (f *FakeStore) RecentTransactions(ctx context.Context, userID int, limit int64) ([]datamodels.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var matches []datamodels.Transaction
	for _, t := range f.Transactions {
		if t.SenderID == userID || t.ReceiverID == userID {
			matches = append(matches, t)
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].DateTimeStamp > matches[j].DateTimeStamp })
	if int64(len(matches)) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func (f *FakeStore) TransactionsInRange(ctx context.Context, userID int, startUnix, endUnix int64) ([]datamodels.Transaction, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var matches []datamodels.Transaction
	for _, t := range f.Transactions {
		if (t.SenderID == userID || t.ReceiverID == userID) && t.DateTimeStamp >= startUnix && t.DateTimeStamp <= endUnix {
			matches = append(matches, t)
		}
	}
	return matches, nil
}
