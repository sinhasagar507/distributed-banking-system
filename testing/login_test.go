package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
)

type LoginRequest struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type TestUser struct {
	UserID   int    `json:"user_id"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func TestLogin(t *testing.T) {
	users := []TestUser{
		{
			UserID:   106,
			Email:    "Tia_Miller@yahoo.com",
			Password: "HnnMY4b34sy1",
		},
		{
			UserID:   110,
			Email:    "Armando_Gorczany20@yahoo.com",
			Password: "OWOs47972FRA",
		},
		{
			UserID:   3,
			Email:    "wronguser@gmail.com",
			Password: "password",
		},
	}

	results := []int{200, 200, 401}

	for idx, user := range users {
		payload := LoginRequest{
			UserID:   strconv.Itoa(user.UserID),
			Email:    user.Email,
			Password: user.Password,
		}

		data, err := json.Marshal(payload)
		if err != nil {
			t.Errorf("Error marshalling JSON: %v", err)
		}

		res, err := http.Post(baseURL()+"/login", "application/json", bytes.NewBuffer(data))
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}

		defer res.Body.Close()

		if res.StatusCode != results[idx] {
			t.Errorf("Expected status code %d, got %d", results[idx], res.StatusCode)
		}

		if res.StatusCode == 200 {
			type Response struct {
				Status string `json:"status"`
			}

			var response Response

			err = json.NewDecoder(res.Body).Decode(&response)
			if err != nil {
				t.Errorf("Error decoding response: %v", err)
			}

			if response.Status != "success" {
				t.Errorf("Expected status 'success', got '%s'", response.Status)
			}
		}
	}
}
