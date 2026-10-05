package service_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"cse512/datamodels"
	"cse512/internal/service"
	"cse512/internal/store"
	"cse512/internal/store/storetest"

	"golang.org/x/crypto/bcrypt"
)

func hash(t *testing.T, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	return string(h)
}

// ---- Login -----------------------------------------------------------------

func TestLogin(t *testing.T) {
	fake := storetest.New()
	fake.Users[106] = datamodels.User{
		UserID: 106, FirstName: "Tia", LastName: "Miller",
		Email: "tia@example.com", Balance: 50000,
		PassHash: hash(t, "correct-password"), AccountNumber: 123456789,
	}
	svc := service.New(fake)

	t.Run("valid credentials succeed", func(t *testing.T) {
		result, err := svc.Login(context.Background(), "106", "tia@example.com", "correct-password")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.UserID != 106 || result.Name != "Tia Miller" || result.Balance != 50000 {
			t.Errorf("unexpected result: %+v", result)
		}
	})

	t.Run("wrong password is rejected", func(t *testing.T) {
		_, err := svc.Login(context.Background(), "106", "tia@example.com", "wrong-password")
		if !errors.Is(err, service.ErrInvalidCredentials) {
			t.Errorf("expected ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("wrong email is rejected", func(t *testing.T) {
		_, err := svc.Login(context.Background(), "106", "someone-else@example.com", "correct-password")
		if !errors.Is(err, service.ErrInvalidCredentials) {
			t.Errorf("expected ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("nonexistent user is rejected", func(t *testing.T) {
		_, err := svc.Login(context.Background(), "999", "nobody@example.com", "whatever")
		if !errors.Is(err, service.ErrLookupFailed) {
			t.Errorf("expected ErrLookupFailed, got %v", err)
		}
	})

	t.Run("non-numeric user_id is rejected, not silently zeroed", func(t *testing.T) {
		_, err := svc.Login(context.Background(), "not-a-number", "tia@example.com", "correct-password")
		if !errors.Is(err, service.ErrLookupFailed) {
			t.Errorf("expected ErrLookupFailed, got %v", err)
		}
	})
}

// ---- PerformTransaction ------------------------------------------------------

func newFakeWithUsers() *storetest.FakeStore {
	fake := storetest.New()
	fake.Users[100] = datamodels.User{UserID: 100, Balance: 10000, AccountNumber: 111}
	fake.Users[101] = datamodels.User{UserID: 101, Balance: 5000, AccountNumber: 222}
	return fake
}

func TestPerformTransaction_AmountRequired(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 100, Amount: 0,
	})

	if outcome.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", outcome.StatusCode)
	}
	if len(fake.Transactions) != 0 {
		t.Errorf("expected no transaction logged, got %d", len(fake.Transactions))
	}
}

func TestPerformTransaction_SenderNotFound_NoFailedRowLogged(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 999, ReceiverID: 100, Amount: 100,
	})

	if outcome.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", outcome.StatusCode)
	}
	if outcome.Result.Message != "Sender not found." {
		t.Errorf("unexpected message: %q", outcome.Result.Message)
	}
	// §1.4: a lookup failure isn't a failed money movement, so it must not
	// pollute transaction history with a "failed" row.
	if len(fake.Transactions) != 0 {
		t.Errorf("expected no transaction logged for a not-found lookup, got %d", len(fake.Transactions))
	}
}

func TestPerformTransaction_ReceiverNotFound_NoFailedRowLogged(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 999, Amount: 100,
	})

	if outcome.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", outcome.StatusCode)
	}
	if len(fake.Transactions) != 0 {
		t.Errorf("expected no transaction logged for a not-found lookup, got %d", len(fake.Transactions))
	}
}

func TestPerformTransaction_AccountNumberMismatch_NoFailedRowLogged(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 101, AccountNumber: 999, Amount: 100,
	})

	if outcome.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", outcome.StatusCode)
	}
	if outcome.Result.Message != "Receiver's account number does not match." {
		t.Errorf("unexpected message: %q", outcome.Result.Message)
	}
	if len(fake.Transactions) != 0 {
		t.Errorf("expected no transaction logged for an account mismatch, got %d", len(fake.Transactions))
	}
}

func TestPerformTransaction_Transfer_Succeeds(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 101, AccountNumber: 222, Amount: 2000,
	})

	if outcome.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %+v", outcome.StatusCode, outcome.Result)
	}
	if fake.Users[100].Balance != 8000 {
		t.Errorf("sender balance = %d, want 8000", fake.Users[100].Balance)
	}
	if fake.Users[101].Balance != 7000 {
		t.Errorf("receiver balance = %d, want 7000", fake.Users[101].Balance)
	}
	if outcome.Result.UpdatedBalance != 8000 {
		t.Errorf("response updated_balance = %d, want 8000", outcome.Result.UpdatedBalance)
	}
}

func TestPerformTransaction_Transfer_InsufficientBalance_IsLogged(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 101, AccountNumber: 222, Amount: 999999,
	})

	if outcome.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", outcome.StatusCode)
	}
	if outcome.Result.Message != "Insufficient balance." {
		t.Errorf("unexpected message: %q", outcome.Result.Message)
	}
	// Unlike the lookup/account-mismatch cases, this IS a genuine failed
	// money-movement attempt, so it should be logged.
	if len(fake.Transactions) != 1 || fake.Transactions[0].Status != "failed" {
		t.Errorf("expected exactly one failed transaction logged, got %+v", fake.Transactions)
	}
	// Balances must be untouched.
	if fake.Users[100].Balance != 10000 || fake.Users[101].Balance != 5000 {
		t.Errorf("balances changed on a rejected transfer: sender=%d receiver=%d", fake.Users[100].Balance, fake.Users[101].Balance)
	}
}

func TestPerformTransaction_SelfDeposit_Succeeds(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 100, AccountNumber: 111, Amount: 500,
	})

	if outcome.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %+v", outcome.StatusCode, outcome.Result)
	}
	if fake.Users[100].Balance != 10500 {
		t.Errorf("balance = %d, want 10500", fake.Users[100].Balance)
	}
}

func TestPerformTransaction_SelfWithdrawal_Succeeds(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 100, AccountNumber: 111, Amount: -3000,
	})

	if outcome.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %+v", outcome.StatusCode, outcome.Result)
	}
	if fake.Users[100].Balance != 7000 {
		t.Errorf("balance = %d, want 7000", fake.Users[100].Balance)
	}
}

// This is the regression test for the exact §1.2 bug: self-withdrawal used
// to skip the funds check entirely (the guard was
// `sender.Balance < amount && senderID != receiverID`), so it could drive a
// balance negative. It must now be rejected like any other overdraw attempt.
func TestPerformTransaction_SelfWithdrawal_InsufficientBalance_IsRejected(t *testing.T) {
	fake := newFakeWithUsers()
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 100, AccountNumber: 111, Amount: -999999,
	})

	if outcome.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 (rejected overdraw), got %d: %+v", outcome.StatusCode, outcome.Result)
	}
	if outcome.Result.Message != "Insufficient balance." {
		t.Errorf("unexpected message: %q", outcome.Result.Message)
	}
	if fake.Users[100].Balance != 10000 {
		t.Errorf("balance changed on a rejected self-withdrawal: got %d, want unchanged 10000", fake.Users[100].Balance)
	}
	if fake.Users[100].Balance < 0 {
		t.Fatalf("balance went negative: %d", fake.Users[100].Balance)
	}
}

func TestPerformTransaction_ReceiverCreditFails_LogsFailedTransaction(t *testing.T) {
	fake := newFakeWithUsers()
	fake.IncrementErr = map[int]error{101: errors.New("simulated write failure")}
	svc := service.New(fake)

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID: 100, ReceiverID: 101, AccountNumber: 222, Amount: 2000,
	})

	if outcome.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %+v", outcome.StatusCode, outcome.Result)
	}
	if outcome.Result.Message != "Failed to update receiver's balance." {
		t.Errorf("unexpected message: %q", outcome.Result.Message)
	}
	if fake.Users[100].Balance != 8000 {
		t.Errorf("fake store has no real rollback, so the debit is still visible in-memory; got %d", fake.Users[100].Balance)
	}
	if len(fake.Transactions) != 1 || fake.Transactions[0].Status != "failed" {
		t.Errorf("expected exactly one failed transaction logged, got %+v", fake.Transactions)
	}
}

// ---- RecentTransactions -----------------------------------------------------

func TestRecentTransactions_LimitsAndOrdersByNewest(t *testing.T) {
	fake := storetest.New()
	fake.Users[100] = datamodels.User{UserID: 100}
	for i := 0; i < 15; i++ {
		fake.Transactions = append(fake.Transactions, datamodels.Transaction{
			SenderID: 100, ReceiverID: 100, DateTimeStamp: int64(i), Status: "completed",
		})
	}
	svc := service.New(fake)

	summaries, err := svc.RecentTransactions(context.Background(), 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(summaries) != 10 {
		t.Fatalf("expected 10 (capped), got %d", len(summaries))
	}
	if summaries[0].TimeStamp != 14 {
		t.Errorf("expected newest first (14), got %d", summaries[0].TimeStamp)
	}
}

func TestRecentTransactions_NilWhenNone(t *testing.T) {
	fake := storetest.New()
	svc := service.New(fake)

	summaries, err := svc.RecentTransactions(context.Background(), 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summaries != nil {
		t.Errorf("expected nil slice (so the JSON response is `null`, not `[]`), got %#v", summaries)
	}
}

// ---- ParseMonthQuery ---------------------------------------------------------

func TestParseMonthQuery(t *testing.T) {
	if _, err := service.ParseMonthQuery(100, "13", "2024"); !errors.Is(err, service.ErrInvalidMonth) {
		t.Errorf("month=13: expected ErrInvalidMonth, got %v", err)
	}
	if _, err := service.ParseMonthQuery(100, "0", "2024"); !errors.Is(err, service.ErrInvalidMonth) {
		t.Errorf("month=0: expected ErrInvalidMonth, got %v", err)
	}
	if _, err := service.ParseMonthQuery(100, "6", "-1"); !errors.Is(err, service.ErrInvalidYear) {
		t.Errorf("year=-1: expected ErrInvalidYear, got %v", err)
	}
	q, err := service.ParseMonthQuery(100, "6", "2024")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.UserID != 100 || q.Start >= q.End {
		t.Errorf("unexpected query: %+v", q)
	}
}

// Sanity check that ErrNotFound round-trips through the fake the same way it
// would through the real store (both use mongo.ErrNoDocuments).
func TestFakeStore_GetUserByID_NotFound(t *testing.T) {
	fake := storetest.New()
	_, err := fake.GetUserByID(context.Background(), 1)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected store.ErrNotFound, got %v", err)
	}
}
