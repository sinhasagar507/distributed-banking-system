package handlers

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"cse512/internal/auth"
	"cse512/internal/httpx"
	"cse512/internal/service"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

func GetMonthData(w http.ResponseWriter, r *http.Request) {
	// The acting user comes from the validated token (auth.Middleware), not
	// a client-supplied user_id query param — §1.3.
	userID, ok := auth.UserIDFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "Missing or invalid authorization token.")
		return
	}

	month := r.URL.Query().Get("month")
	if month == "" {
		httpx.Error(w, http.StatusBadRequest, "month is required")
		return
	}

	year := r.URL.Query().Get("year")
	if year == "" {
		httpx.Error(w, http.StatusBadRequest, "year is required")
		return
	}

	query, err := service.ParseMonthQuery(userID, month, year)
	if err != nil {
		if errors.Is(err, service.ErrInvalidMonth) {
			httpx.Error(w, http.StatusBadRequest, "invalid month provided")
		} else {
			httpx.Error(w, http.StatusBadRequest, "invalid year provided")
		}
		return
	}

	transactions, err := svc.MonthlyTransactions(context.Background(), query)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, fmt.Sprintf("error querying database: %v", err))
		return
	}

	if len(transactions) == 0 {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "You have not made any transactions this month."}`))
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=transactions.csv")
	w.Header().Set("Content-Type", "text/csv")

	writer := csv.NewWriter(w)

	err = writer.Write([]string{"Sender ID", "Receiver ID", "Amount", "Remarks", "Date", "Status"})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, fmt.Sprintf("error writing CSV header: %v", err))
		return
	}

	p := message.NewPrinter(language.English)

	for _, transaction := range transactions {
		// Amount is integer cents (§1.2); divide by 100 for the dollar display.
		formattedAmount := p.Sprintf("$%.2f", float64(transaction.Amount)/100)

		err := writer.Write([]string{
			strconv.Itoa(transaction.SenderID),
			strconv.Itoa(transaction.ReceiverID),
			formattedAmount,
			transaction.Remarks,
			transaction.DateTimeStamp,
			transaction.Status,
		})
		if err != nil {
			httpx.Error(w, http.StatusInternalServerError, fmt.Sprintf("error writing CSV row: %v", err))
			return
		}
	}

	writer.Flush()

	if err := writer.Error(); err != nil {
		httpx.Error(w, http.StatusInternalServerError, fmt.Sprintf("error flushing CSV data: %v", err))
		return
	}
}
