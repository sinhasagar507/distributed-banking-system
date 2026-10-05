package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"cse512/internal/auth"
	"cse512/internal/httpx"
	"cse512/internal/service"
)

// HandleLogin processes user login requests
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	var credentials struct {
		UserID   string `json:"user_id"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
		httpx.Error(w, http.StatusBadRequest, "Failed to parse JSON.")
		return
	}

	if credentials.UserID == "" || credentials.Email == "" || credentials.Password == "" {
		httpx.Error(w, http.StatusBadRequest, "Missing required fields (user_id, email, password).")
		return
	}

	result, err := svc.Login(context.Background(), credentials.UserID, credentials.Email, credentials.Password)
	if err != nil {
		message := "Error fetching details. Please try again."
		if errors.Is(err, service.ErrInvalidCredentials) {
			message = "Invalid credentials. Please try again."
		}
		httpx.Error(w, http.StatusUnauthorized, message)
		return
	}

	token, err := auth.IssueToken(result.UserID)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "Failed to issue session token.")
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(Response{
		Status:  "success",
		Message: "Login successful.",
		Data: map[string]any{
			"user_id":        result.UserID,
			"email":          result.Email,
			"name":           result.Name,
			"balance":        result.Balance,
			"account_number": result.AccountNumber,
			"token":          token,
		},
	})
}
