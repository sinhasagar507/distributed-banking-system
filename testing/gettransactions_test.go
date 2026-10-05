package main

import (
	"bytes"
	"cse512/handlers"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestGetTransaction(t *testing.T) {

	// §1.3: /transactions now requires a bearer token and always acts as the
	// logged-in user (the old ?sender_id= query param is ignored), so log in
	// as user 106 first to get one.
	loginPayload, err := json.Marshal(LoginRequest{
		UserID:   "106",
		Email:    "Tia_Miller@yahoo.com",
		Password: "HnnMY4b34sy1",
	})
	if err != nil {
		t.Fatalf("Error marshalling login JSON: %v", err)
	}
	loginRes, err := http.Post(baseURL()+"/login", "application/json", bytes.NewBuffer(loginPayload))
	if err != nil {
		t.Fatalf("Login request failed: %v", err)
	}
	defer loginRes.Body.Close()

	var loginResponse struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(loginRes.Body).Decode(&loginResponse); err != nil {
		t.Fatalf("Error decoding login response: %v", err)
	}

	// Expected amounts/status come from the two most recent (by dateTimeStamp)
	// transactions involving user 106 in the deterministic seed sample
	// (scripts/seed/sample/transactions.json, generated via
	// scripts/seed/generate.js --seed 512). Amounts are integer cents (§1.2).
	results := []struct {
		status string
		amount int
	}{
		{
			status: "completed",
			amount: 643292,
		},
		{
			status: "completed",
			amount: -1236705,
		},
	}

	req, err := http.NewRequest("GET", baseURL()+"/transactions", nil)
	if err != nil {
		t.Fatalf("Error building request: %v", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", loginResponse.Data.Token))

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer res.Body.Close()

	var finalResponse []handlers.TransactionResponse

	err = json.NewDecoder(res.Body).Decode(&finalResponse)
	if err != nil {
		t.Errorf("Error decoding response: %v", err)
	}

	for idx, response := range finalResponse {
		if idx >= 2 {
			break
		}

		if response.Amount != results[idx].amount {
			t.Errorf("Expected amount %d, got %d", results[idx].amount, response.Amount)
		}

		if response.Status != results[idx].status {
			t.Errorf("Expected status %s, got %s", results[idx].status, response.Status)
		}
	}
}
