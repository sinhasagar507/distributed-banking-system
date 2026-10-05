package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// TestConcurrentTransfers_ConservesMoney is the automated version of the
// manual proof done while building IMPROVEMENT_PLAN §1.2: fire N simultaneous
// transfers that each individually exceed half the sender's balance, so at
// most one can legitimately succeed. Before the §1.2 fix (plain
// check-then-$inc, no atomic guard, writes outside the session), this would
// have let more than one succeed and overdrawn the account — this test would
// have failed against the pre-fix code. Requires the real compose stack: the
// guarantee under test is Mongo's actual transaction/conditional-update
// behavior, not something a unit test with a fake store can exercise (that's
// §2.1's job, which deliberately stays out of this territory).
func TestConcurrentTransfers_ConservesMoney(t *testing.T) {
	const (
		senderID      = 110
		senderEmail   = "Armando_Gorczany20@yahoo.com"
		senderPass    = "OWOs47972FRA"
		receiverID    = 111
		receiverEmail = "Carolyne.Price@yahoo.com"
		receiverPass  = "s6JT7LH1btLm"
		receiverAcct  = 464422652

		concurrentRequests = 10
	)

	senderToken, senderBalanceBefore := loginForBalance(t, senderID, senderEmail, senderPass)
	_, receiverBalanceBefore := loginForBalance(t, receiverID, receiverEmail, receiverPass)

	if senderBalanceBefore <= 0 {
		t.Fatalf("test fixture assumption broken: sender balance is %d, need a positive balance to transfer from", senderBalanceBefore)
	}

	// 60% of the CURRENT balance, not a fixed cents value: each successful
	// run permanently debits the seed data, so a fixed amount would make this
	// test pass once and then fail on every rerun against the same
	// persisted stack as the balance depletes below it. Scaling to the
	// current balance keeps it repeatable; 60%*2 > 100% still guarantees at
	// most one of the concurrent transfers can succeed.
	transferAmount := senderBalanceBefore * 6 / 10
	if transferAmount == 0 {
		t.Fatalf("sender balance %d too small to produce a nonzero transfer amount", senderBalanceBefore)
	}

	var wg sync.WaitGroup
	statusCodes := make([]int, concurrentRequests)

	for i := 0; i < concurrentRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			payload, err := json.Marshal(map[string]any{
				"receiver_id":    receiverID,
				"account_number": receiverAcct,
				"amount":         transferAmount,
				"remarks":        fmt.Sprintf("concurrency test %d", idx),
				"dateTimeStamp":  1700005000 + idx,
			})
			if err != nil {
				t.Errorf("marshal request %d: %v", idx, err)
				return
			}

			req, err := http.NewRequest("POST", baseURL()+"/transaction", bytes.NewBuffer(payload))
			if err != nil {
				t.Errorf("build request %d: %v", idx, err)
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+senderToken)

			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Errorf("request %d failed: %v", idx, err)
				return
			}
			defer res.Body.Close()
			statusCodes[idx] = res.StatusCode
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, code := range statusCodes {
		if code == http.StatusOK {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 of %d concurrent transfers to succeed, got %d (status codes: %v)",
			concurrentRequests, successes, statusCodes)
	}

	_, senderBalanceAfter := loginForBalance(t, senderID, senderEmail, senderPass)
	_, receiverBalanceAfter := loginForBalance(t, receiverID, receiverEmail, receiverPass)

	if senderBalanceAfter < 0 {
		t.Errorf("sender balance went negative: %d", senderBalanceAfter)
	}

	totalBefore := senderBalanceBefore + receiverBalanceBefore
	totalAfter := senderBalanceAfter + receiverBalanceAfter
	if totalBefore != totalAfter {
		t.Errorf("money not conserved: total before=%d, total after=%d (diff=%d)",
			totalBefore, totalAfter, totalAfter-totalBefore)
	}

	wantSenderAfter := senderBalanceBefore - transferAmount
	if senderBalanceAfter != wantSenderAfter {
		t.Errorf("sender balance after = %d, want %d (exactly one debit)", senderBalanceAfter, wantSenderAfter)
	}
}

// loginForBalance logs in as the given user and returns (token, balance).
func loginForBalance(t *testing.T, userID int, email, password string) (string, int) {
	t.Helper()

	payload, err := json.Marshal(LoginRequest{
		UserID:   fmt.Sprintf("%d", userID),
		Email:    email,
		Password: password,
	})
	if err != nil {
		t.Fatalf("marshal login payload: %v", err)
	}

	res, err := http.Post(baseURL()+"/login", "application/json", bytes.NewBuffer(payload))
	if err != nil {
		t.Fatalf("login request failed: %v", err)
	}
	defer res.Body.Close()

	var response struct {
		Data struct {
			Token   string `json:"token"`
			Balance int    `json:"balance"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if response.Data.Token == "" {
		t.Fatalf("login for user %d did not return a token (status %d)", userID, res.StatusCode)
	}

	return response.Data.Token, response.Data.Balance
}
