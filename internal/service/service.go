// Package service holds DISBank's business logic (money movement, txn-type
// classification, balance rules) extracted out of the HTTP handlers
// (IMPROVEMENT_PLAN §1.1). It depends on store.Store, not the mongo driver,
// so it can be unit tested with a fake store.
//
// PerformTransaction's money-movement path was hardened in §1.2: writes now
// run inside a genuine Mongo transaction (store.Store.RunInTransaction), the
// balance check is an atomic conditional $inc (store.Store.DebitIfSufficient)
// instead of read-then-write, self-withdrawals go through the same guard as
// transfers, and balance-critical reads use a primary/majority read
// preference instead of the client's default secondary. The ignored
// balance-refetch error on the final response is intentionally still
// ignored — see the comment at that call site.
package service

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"cse512/datamodels"
	"cse512/internal/httpx"
	"cse512/internal/logging"
	"cse512/internal/store"

	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	Store store.Store
}

func New(s store.Store) *Service {
	return &Service{Store: s}
}

// ---- Login (handlers/login.go) --------------------------------------------

type LoginResult struct {
	UserID        int
	Email         string
	Name          string
	Balance       int
	AccountNumber int64
}

var (
	ErrLookupFailed       = errors.New("error fetching details")
	ErrInvalidCredentials = errors.New("invalid credentials")
)

// Login hardens the original handler's ignored strconv.Atoi error (§1.3): a
// non-numeric user_id is now ErrLookupFailed instead of silently becoming 0
// and almost certainly also failing the lookup — same observable response,
// but no longer by accident. UserID is now the parsed int (needed to issue a
// JWT) rather than passing the input string straight through.
func (s *Service) Login(ctx context.Context, userIDStr, email, password string) (LoginResult, error) {
	userID, err := strconv.Atoi(userIDStr)
	if err != nil {
		return LoginResult{}, ErrLookupFailed
	}

	user, err := s.Store.GetUserByID(ctx, userID)
	if err != nil {
		return LoginResult{}, ErrLookupFailed
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PassHash), []byte(password)) != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	if user.Email != email {
		return LoginResult{}, ErrInvalidCredentials
	}

	return LoginResult{
		UserID:        userID,
		Email:         email,
		Name:          user.FirstName + " " + user.LastName,
		Balance:       user.Balance,
		AccountNumber: user.AccountNumber,
	}, nil
}

// ---- Recent transactions (handlers/transactions.go) ------------------------

type TransactionSummary struct {
	Status    string `json:"status"`
	Amount    int    `json:"amount"`
	TimeStamp int64  `json:"dateTimeStamp"`
	Remarks   string `json:"remarks"`
}

// RecentTransactions returns a user's 10 most recent transactions (sender or
// receiver), newest first. Returns a nil slice (not empty) when there are
// none, matching the original handler's un-initialized slice so the JSON
// response stays `null`, not `[]`.
func (s *Service) RecentTransactions(ctx context.Context, userID int) ([]TransactionSummary, error) {
	txns, err := s.Store.RecentTransactions(ctx, userID, 10)
	if err != nil {
		return nil, err
	}

	var out []TransactionSummary
	for _, t := range txns {
		out = append(out, TransactionSummary{
			Status:    t.Status,
			Amount:    t.Amount,
			TimeStamp: t.DateTimeStamp,
			Remarks:   t.Remarks,
		})
	}
	return out, nil
}

// ---- Monthly transactions (handlers/monthdata.go) ---------------------------

var (
	ErrInvalidMonth = errors.New("invalid month provided")
	ErrInvalidYear  = errors.New("invalid year provided")
)

type MonthQuery struct {
	UserID int
	Start  int64
	End    int64
}

// ParseMonthQuery validates and converts the month-data endpoint's month/year
// query params into a Mongo timestamp range, matching the original handler's
// validation rules (1-12 for month, non-negative year) and error messages.
// userID comes from the authenticated token (§1.3), not a query param.
func ParseMonthQuery(userID int, monthStr, yearStr string) (MonthQuery, error) {
	monthInt, err := strconv.Atoi(monthStr)
	if err != nil || monthInt < 1 || monthInt > 12 {
		return MonthQuery{}, ErrInvalidMonth
	}

	yearInt, err := strconv.Atoi(yearStr)
	if err != nil || yearInt < 0 {
		return MonthQuery{}, ErrInvalidYear
	}

	startDate := time.Date(yearInt, time.Month(monthInt), 1, 0, 0, 0, 0, time.UTC)
	endDate := startDate.AddDate(0, 1, 0).Add(-time.Second)

	return MonthQuery{UserID: userID, Start: startDate.Unix(), End: endDate.Unix()}, nil
}

type MonthlyTransaction struct {
	SenderID      int
	ReceiverID    int
	Amount        int
	Remarks       string
	DateTimeStamp string // formatted "02 Jan 2006", matching the original CSV column
	Status        string
}

func (s *Service) MonthlyTransactions(ctx context.Context, q MonthQuery) ([]MonthlyTransaction, error) {
	txns, err := s.Store.TransactionsInRange(ctx, q.UserID, q.Start, q.End)
	if err != nil {
		return nil, err
	}

	var out []MonthlyTransaction
	for _, t := range txns {
		out = append(out, MonthlyTransaction{
			SenderID:      t.SenderID,
			ReceiverID:    t.ReceiverID,
			Amount:        t.Amount,
			Remarks:       t.Remarks,
			DateTimeStamp: time.Unix(t.DateTimeStamp, 0).Format("02 Jan 2006"),
			Status:        t.Status,
		})
	}
	return out, nil
}

// ---- PerformTransaction (handlers/handletransaction.go) --------------------

type TransactionRequest struct {
	SenderID      int
	ReceiverID    int
	AccountNumber int
	Amount        int
	Remarks       string
	Timestamp     int64
}

type TransactionResult struct {
	Status         string `json:"status"`
	Message        string `json:"message"`
	UpdatedBalance int    `json:"updated_balance"`
}

type TransactionOutcome struct {
	StatusCode int
	Result     TransactionResult
}

// PerformTransaction executes a deposit, withdrawal, or transfer
// (self-transactions are sender_id == receiver_id; see CLAUDE.md), preserving
// the original handler's exact validation order, status codes, and messages.
func (s *Service) PerformTransaction(ctx context.Context, req TransactionRequest) TransactionOutcome {
	logFailed := func() {
		_ = s.Store.InsertTransaction(ctx, datamodels.Transaction{
			SenderID:      req.SenderID,
			ReceiverID:    req.ReceiverID,
			Amount:        req.Amount,
			Remarks:       req.Remarks,
			DateTimeStamp: req.Timestamp,
			Status:        "failed",
		})
		logging.Logger.Info("failed transaction logged", "request_id", httpx.RequestID(ctx), "sender_id", req.SenderID, "receiver_id", req.ReceiverID)
	}

	if req.Amount == 0 {
		return TransactionOutcome{http.StatusBadRequest, TransactionResult{
			Status: "error", Message: "Amount is required.",
		}}
	}

	// Primary/majority reads for balance-critical lookups (§1.2): the client's
	// default read preference is Secondary, which could return a stale
	// balance, a false "not found", or a stale account number on a lagging
	// replica.
	// §1.4: a lookup failure isn't a failed money movement — nothing was
	// attempted yet — so unlike the branches below, this doesn't log a
	// "failed" transactions row. Previously it did, which polluted
	// transaction history with non-transactions (e.g. a typo'd sender_id).
	sender, err := s.Store.GetUserByIDStrong(ctx, req.SenderID)
	if err != nil {
		msg := "Failed to fetch sender's data."
		if errors.Is(err, store.ErrNotFound) {
			msg = "Sender not found."
		}
		return TransactionOutcome{http.StatusNotFound, TransactionResult{
			Status: "error", Message: msg, UpdatedBalance: sender.Balance,
		}}
	}

	// Fast-path rejection so a transfer with an obviously-insufficient
	// balance doesn't even look up the receiver. Not authoritative by
	// itself — see the DebitIfSufficient guard below, which is what actually
	// closes the check-then-act race. Also still skips self-withdrawals
	// (senderID == receiverID): that gap is closed below too, instead of
	// here, since this fast path couldn't see a concurrent debit anyway.
	if sender.Balance < req.Amount && req.SenderID != req.ReceiverID {
		logFailed()
		return TransactionOutcome{http.StatusBadRequest, TransactionResult{
			Status: "error", Message: "Insufficient balance.", UpdatedBalance: sender.Balance,
		}}
	}

	// Same §1.4 reasoning: receiver-not-found and an account-number mismatch
	// are request-validation failures, not failed money movements — no
	// "failed" row.
	receiver, err := s.Store.GetUserByIDStrong(ctx, req.ReceiverID)
	if err != nil {
		msg := "Failed to fetch receiver's data."
		if errors.Is(err, store.ErrNotFound) {
			msg = "Receiver not found."
		}
		return TransactionOutcome{http.StatusNotFound, TransactionResult{
			Status: "error", Message: msg, UpdatedBalance: sender.Balance,
		}}
	}

	if receiver.AccountNumber != int64(req.AccountNumber) {
		return TransactionOutcome{http.StatusBadRequest, TransactionResult{
			Status: "error", Message: "Receiver's account number does not match.",
		}}
	}

	// Everything below runs inside session.WithTransaction (via
	// RunInTransaction), so the writes are genuinely atomic: every store call
	// made with sessCtx either all commit or all roll back together. The
	// previous version started a session but issued these same $inc calls
	// with context.Background(), so they ran as independent autocommit
	// writes the session had no power to roll back — that was the core bug.
	var insufficientFunds bool
	var failureMessage string

	txErr := s.Store.RunInTransaction(ctx, func(sessCtx context.Context) error {
		// Reset on each attempt: WithTransaction retries the callback on
		// transient errors, so a stale value from an earlier attempt must
		// not leak into the final outcome.
		insufficientFunds = false
		failureMessage = ""

		if req.SenderID == req.ReceiverID {
			if req.Amount > 0 {
				if err := s.Store.IncrementBalance(sessCtx, req.ReceiverID, req.Amount); err != nil {
					failureMessage = "Failed to update balance (deposit)."
					return err
				}
				return nil
			}
			// Self-withdrawal: now goes through the same atomic $gte guard
			// as a transfer debit. Originally this branch had no funds check
			// at all (the pre-check above only ran when senderID !=
			// receiverID), so a self-withdrawal could drive the balance
			// negative — that bug is fixed here.
			if err := s.Store.DebitIfSufficient(sessCtx, req.SenderID, -req.Amount); err != nil {
				if errors.Is(err, store.ErrInsufficientFunds) {
					insufficientFunds = true
				} else {
					failureMessage = "Failed to update balance (withdrawal)."
				}
				return err
			}
			return nil
		}

		if err := s.Store.DebitIfSufficient(sessCtx, req.SenderID, req.Amount); err != nil {
			if errors.Is(err, store.ErrInsufficientFunds) {
				insufficientFunds = true
			} else {
				failureMessage = "Failed to update sender's balance."
			}
			return err
		}
		if err := s.Store.IncrementBalance(sessCtx, req.ReceiverID, req.Amount); err != nil {
			failureMessage = "Failed to update receiver's balance."
			return err
		}
		return nil
	})

	if txErr != nil {
		logFailed()
		if insufficientFunds {
			return TransactionOutcome{http.StatusBadRequest, TransactionResult{
				Status: "error", Message: "Insufficient balance.", UpdatedBalance: sender.Balance,
			}}
		}
		if failureMessage == "" {
			failureMessage = "Failed to commit transaction."
		}
		return TransactionOutcome{http.StatusInternalServerError, TransactionResult{
			Status: "error", Message: failureMessage, UpdatedBalance: sender.Balance,
		}}
	}

	if err := s.Store.InsertTransaction(ctx, datamodels.Transaction{
		SenderID:      req.SenderID,
		ReceiverID:    req.ReceiverID,
		Amount:        req.Amount,
		Remarks:       req.Remarks,
		DateTimeStamp: req.Timestamp,
		Status:        "success",
	}); err != nil {
		return TransactionOutcome{http.StatusInternalServerError, TransactionResult{
			Status: "error", Message: "Failed to log transaction.", UpdatedBalance: sender.Balance,
		}}
	}

	// Re-fetch the sender's balance for the response with a primary/majority
	// read, so updated_balance is never stale (§1.2's "stale success
	// balance" bug); on error, keep the pre-transaction balance already in
	// `sender` (ignored error, matching the original handler's unchecked
	// re-fetch).
	if fresh, err := s.Store.GetUserByIDStrong(ctx, req.SenderID); err == nil {
		sender = fresh
	}

	return TransactionOutcome{http.StatusOK, TransactionResult{
		Status: "success", Message: "Transaction completed successfully.", UpdatedBalance: sender.Balance,
	}}
}
