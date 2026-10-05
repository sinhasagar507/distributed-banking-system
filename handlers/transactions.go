package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"cse512/internal/auth"
	"cse512/internal/httpx"
)

// TransactionResponse represents the response structure for the transaction handler
type TransactionResponse struct {
	Status    string `json:"status"`
	Amount    int    `json:"amount"`
	TimeStamp int64  `json:"dateTimeStamp"`
	Remarks   string `json:"remarks"`
}

// HandleTransaction handles requests for retrieving user transactions
func HandleTransaction(w http.ResponseWriter, r *http.Request) {
	// The acting user comes from the validated token (auth.Middleware), not
	// any client-supplied sender_id — §1.3 closes the hole where a client
	// could read anyone's transaction history by passing a different id.
	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "Missing or invalid authorization token.")
		return
	}

	summaries, err := svc.RecentTransactions(context.Background(), userID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Failed to fetch transactions.")
		return
	}

	var transactions []TransactionResponse
	for _, s := range summaries {
		transactions = append(transactions, TransactionResponse{
			Status:    s.Status,
			Amount:    s.Amount,
			TimeStamp: s.TimeStamp,
			Remarks:   s.Remarks,
		})
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(transactions)
}
