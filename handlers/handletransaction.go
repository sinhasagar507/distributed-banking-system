package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"cse512/internal/auth"
	"cse512/internal/httpx"
	"cse512/internal/service"
)

// PerformTransaction handles a transaction between sender and receiver (withdraw, deposit, or transfer)
func PerformTransaction(w http.ResponseWriter, r *http.Request) {
	// The acting sender comes from the validated token (auth.Middleware), not
	// the client-supplied sender_id — §1.3 closes the hole where a client
	// could move money out of anyone's account by passing a different id.
	// receiver_id is still client-supplied: you can send to anyone, you just
	// can't act as anyone but yourself.
	senderID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "Missing or invalid authorization token.")
		return
	}

	var transaction struct {
		ReceiverID    int    `json:"receiver_id"`
		AccountNumber int    `json:"account_number"`
		Amount        int    `json:"amount"`
		Remarks       string `json:"remarks"`
		Timestamp     int64  `json:"dateTimeStamp"`
	}

	if err := json.NewDecoder(r.Body).Decode(&transaction); err != nil {
		httpx.Error(w, http.StatusBadRequest, "Failed to parse JSON.")
		return
	}

	outcome := svc.PerformTransaction(context.Background(), service.TransactionRequest{
		SenderID:      senderID,
		ReceiverID:    transaction.ReceiverID,
		AccountNumber: transaction.AccountNumber,
		Amount:        transaction.Amount,
		Remarks:       transaction.Remarks,
		Timestamp:     transaction.Timestamp,
	})

	w.WriteHeader(outcome.StatusCode)
	json.NewEncoder(w).Encode(outcome.Result)
}
