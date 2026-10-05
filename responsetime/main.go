// Package main is DISBank's load-testing harness (IMPROVEMENT_PLAN §3.3).
//
// Earlier version: you picked a scenario by editing main() and uncommenting a
// call, requests were spread over per-instance ports 8080-8085 via a shared
// counter mutated by every worker goroutine without synchronization (a data
// race that also skewed the distribution), rand.Seed was called inside the
// per-iteration loop, and only an average latency was reported with results
// going to stdout only.
//
// This version: scenario/concurrency/volume/target are CLI flags; each worker
// writes only to its own slice (no shared mutable state, no race); latency
// percentiles (p50/p95/p99, not just average) are computed over completed
// requests; every run appends one row to a CSV file for §3.4's sweep to
// chart; traffic goes through nginx's single published entrypoint by default
// so the load balancer's round-robin across app instances is exercised, not
// bypassed.
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// loginCreds are the deterministic seed-data users (scripts/seed/generate.js
// --seed 512; see scripts/seed/sample/credentials.json), same dataset the
// integration tests use.
type loginCreds struct {
	UserID   string
	Email    string
	Password string
}

var seedUsers = []loginCreds{
	{UserID: "100", Email: "Coty.Hickle@gmail.com", Password: "9kvmJHTuiXPS"},
	{UserID: "101", Email: "Stuart.Beahan31@gmail.com", Password: "0w7mGoozqZPK"},
	{UserID: "102", Email: "Aniya.Kohler30@gmail.com", Password: "vPnt5ouRKEoK"},
	{UserID: "103", Email: "Keyshawn.Dickinson@gmail.com", Password: "Ss9zqeDAEXwZ"},
	{UserID: "104", Email: "Thaddeus_Orn-Volkman@gmail.com", Password: "icdcASd9sdnw"},
	{UserID: "105", Email: "Esta_Stoltenberg@hotmail.com", Password: "OBt7g6mseJZG"},
	{UserID: "106", Email: "Tia_Miller@yahoo.com", Password: "HnnMY4b34sy1"},
	{UserID: "107", Email: "Alexys.Zieme@hotmail.com", Password: "2YQoYlrAXZ3R"},
	{UserID: "108", Email: "Jarred_Torp@gmail.com", Password: "AuuBoLRykE8Q"},
	{UserID: "109", Email: "Yazmin.Blanda@hotmail.com", Password: "VmIRFmsvm0N5"},
	{UserID: "110", Email: "Armando_Gorczany20@yahoo.com", Password: "OWOs47972FRA"},
}

type apiResponse struct {
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data"`
}

var httpClient = &http.Client{Timeout: 10 * time.Second}

// runLoad fans a total of n calls to fn out across workers concurrent
// goroutines. Each worker only ever writes to its own slot in results/errs —
// disjoint memory, so there is nothing to synchronize (this is the fix for
// the old shared, unsynchronized counter).
func runLoad(workers, n int, fn func(workerID int) (time.Duration, error)) (latencies []time.Duration, errCount int, wall time.Duration) {
	perWorker := n / workers
	remainder := n % workers

	results := make([][]time.Duration, workers)
	errs := make([]int, workers)

	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		count := perWorker
		if w < remainder {
			count++
		}
		wg.Add(1)
		go func(workerID, count int) {
			defer wg.Done()
			local := make([]time.Duration, 0, count)
			localErrs := 0
			for i := 0; i < count; i++ {
				d, err := fn(workerID)
				if err != nil {
					localErrs++
					continue
				}
				local = append(local, d)
			}
			results[workerID] = local
			errs[workerID] = localErrs
		}(w, count)
	}
	wg.Wait()
	wall = time.Since(start)

	for w := range results {
		latencies = append(latencies, results[w]...)
		errCount += errs[w]
	}
	return latencies, errCount, wall
}

func doLogin(baseURL string, creds loginCreds) (*apiResponse, int, error) {
	payload := struct {
		UserID   string `json:"user_id"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}{UserID: creds.UserID, Email: creds.Email, Password: creds.Password}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}

	resp, err := httpClient.Post(baseURL+"/login", "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var out apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, resp.StatusCode, err
	}
	return &out, resp.StatusCode, nil
}

// authenticateAll logs in every seed user once, up front, outside the timed
// load loop — the transaction/monthly scenarios need a bearer token (§1.3
// put every non-login endpoint behind JWT auth), but pre-auth latency isn't
// what those scenarios measure.
func authenticateAll(baseURL string) ([]string, error) {
	tokens := make([]string, len(seedUsers))
	for i, creds := range seedUsers {
		out, status, err := doLogin(baseURL, creds)
		if err != nil {
			return nil, fmt.Errorf("pre-auth login for %s: %w", creds.Email, err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("pre-auth login for %s: status %d", creds.Email, status)
		}
		token, _ := out.Data["token"].(string)
		if token == "" {
			return nil, fmt.Errorf("pre-auth login for %s: response carried no token", creds.Email)
		}
		tokens[i] = token
	}
	return tokens, nil
}

func runLoginTest(baseURL string, workers, n int) ([]time.Duration, int, time.Duration) {
	return runLoad(workers, n, func(workerID int) (time.Duration, error) {
		creds := seedUsers[workerID%len(seedUsers)]
		start := time.Now()
		_, status, err := doLogin(baseURL, creds)
		dur := time.Since(start)
		if err != nil {
			return 0, err
		}
		if status != http.StatusOK {
			return 0, fmt.Errorf("status %d", status)
		}
		return dur, nil
	})
}

// runTransactionTest exercises GET /transactions (recent-transaction lookup).
// Pre-§1.3 this hit the same route with a client-supplied sender_id query
// param, mostly against nonexistent users; now the acting user comes from
// each worker's own bearer token.
func runTransactionTest(baseURL string, workers, n int) ([]time.Duration, int, time.Duration) {
	tokens, err := authenticateAll(baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pre-auth failed: %v\n", err)
		os.Exit(1)
	}
	return runLoad(workers, n, func(workerID int) (time.Duration, error) {
		token := tokens[workerID%len(tokens)]
		req, err := http.NewRequest(http.MethodGet, baseURL+"/transactions", nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		start := time.Now()
		resp, err := httpClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		dur := time.Since(start)

		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("status %d", resp.StatusCode)
		}
		return dur, nil
	})
}

// runMonthlyTest exercises GET /monthdata. Pre-§1.3 this also randomized
// user_id over 100-200000, almost all of which don't exist in the 11-user
// seed dataset, so nearly every request already failed before measuring
// anything useful. The acting user now comes from the token; only month/year
// are randomized (or pinned via -month/-year for a repeatable run).
func runMonthlyTest(baseURL string, workers, n, monthOverride, yearOverride int) ([]time.Duration, int, time.Duration) {
	tokens, err := authenticateAll(baseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pre-auth failed: %v\n", err)
		os.Exit(1)
	}
	return runLoad(workers, n, func(workerID int) (time.Duration, error) {
		token := tokens[workerID%len(tokens)]

		month := monthOverride
		if month == 0 {
			month = rand.Intn(12) + 1
		}
		year := yearOverride
		if year == 0 {
			year = rand.Intn(5) + 2020 // 2020-2024, matching the seed data's date range
		}

		url := fmt.Sprintf("%s/monthdata?month=%d&year=%d", baseURL, month, year)
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		start := time.Now()
		resp, err := httpClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		dur := time.Since(start)

		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("status %d", resp.StatusCode)
		}
		return dur, nil
	})
}

// percentile returns the p-th percentile (0-100) of a slice already sorted
// ascending, using the nearest-rank method.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func ms(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

type summary struct {
	scenario   string
	workers    int
	requests   int
	instances  int
	baseURL    string
	p50, p95   time.Duration
	p99        time.Duration
	mean       time.Duration
	errCount   int
	throughput float64 // successful requests per second, over wall-clock time
}

func summarize(scenario string, workers, requests, instances int, baseURL string, latencies []time.Duration, errCount int, wall time.Duration) summary {
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var total time.Duration
	for _, d := range sorted {
		total += d
	}
	var mean time.Duration
	if len(sorted) > 0 {
		mean = total / time.Duration(len(sorted))
	}

	return summary{
		scenario:   scenario,
		workers:    workers,
		requests:   requests,
		instances:  instances,
		baseURL:    baseURL,
		p50:        percentile(sorted, 50),
		p95:        percentile(sorted, 95),
		p99:        percentile(sorted, 99),
		mean:       mean,
		errCount:   errCount,
		throughput: float64(len(sorted)) / wall.Seconds(),
	}
}

func (s summary) printReport() {
	fmt.Printf("\n%s test\n", s.scenario)
	fmt.Printf("  target:              %s\n", s.baseURL)
	fmt.Printf("  workers:             %d\n", s.workers)
	fmt.Printf("  requests (planned):  %d\n", s.requests)
	fmt.Printf("  errors:              %d\n", s.errCount)
	fmt.Printf("  mean latency:        %.3f ms\n", ms(s.mean))
	fmt.Printf("  p50 latency:         %.3f ms\n", ms(s.p50))
	fmt.Printf("  p95 latency:         %.3f ms\n", ms(s.p95))
	fmt.Printf("  p99 latency:         %.3f ms\n", ms(s.p99))
	fmt.Printf("  throughput:          %.2f req/s (completed requests)\n", s.throughput)
}

// appendCSV appends one summary row to path, writing the header first if the
// file doesn't exist yet. Columns are the ones §3.4's sweep needs to chart
// throughput/tail-latency vs. instance count.
func appendCSV(path string, s summary) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	_, statErr := os.Stat(path)
	needsHeader := os.IsNotExist(statErr)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	if needsHeader {
		if err := w.Write([]string{
			"timestamp", "scenario", "workers", "requests", "instances", "base_url",
			"p50_ms", "p95_ms", "p99_ms", "mean_ms", "errors", "throughput_rps",
		}); err != nil {
			return err
		}
	}
	row := []string{
		time.Now().UTC().Format(time.RFC3339),
		s.scenario,
		strconv.Itoa(s.workers),
		strconv.Itoa(s.requests),
		strconv.Itoa(s.instances),
		s.baseURL,
		strconv.FormatFloat(ms(s.p50), 'f', 3, 64),
		strconv.FormatFloat(ms(s.p95), 'f', 3, 64),
		strconv.FormatFloat(ms(s.p99), 'f', 3, 64),
		strconv.FormatFloat(ms(s.mean), 'f', 3, 64),
		strconv.Itoa(s.errCount),
		strconv.FormatFloat(s.throughput, 'f', 2, 64),
	}
	if err := w.Write(row); err != nil {
		return err
	}
	w.Flush()
	return w.Error()
}

func main() {
	scenario := flag.String("test", "login", "scenario to run: login|transaction|monthly")
	workers := flag.Int("workers", 10, "concurrent workers")
	n := flag.Int("n", 100, "total requests to send")
	baseURL := flag.String("base-url", "http://localhost:8080", "target base URL — defaults to nginx's published entrypoint (round-robins app-1/2/3); point at e.g. http://localhost:8081 to hit one bare-metal instance directly")
	month := flag.Int("month", 0, "pin the month (1-12) for the monthly scenario; 0 = random per request")
	year := flag.Int("year", 0, "pin the year for the monthly scenario; 0 = random per request (2020-2024)")
	instances := flag.Int("instances", 0, "informational only: how many app instances were up for this run, recorded in the CSV so §3.4's sweep can chart throughput/latency vs. instance count")
	csvPath := flag.String("csv", "benchmarks/results.csv", "CSV file this run's summary row is appended to")
	flag.Parse()

	if *workers <= 0 || *n <= 0 {
		fmt.Fprintln(os.Stderr, "-workers and -n must both be positive")
		os.Exit(1)
	}

	var latencies []time.Duration
	var errCount int
	var wall time.Duration

	switch *scenario {
	case "login":
		latencies, errCount, wall = runLoginTest(*baseURL, *workers, *n)
	case "transaction":
		latencies, errCount, wall = runTransactionTest(*baseURL, *workers, *n)
	case "monthly":
		latencies, errCount, wall = runMonthlyTest(*baseURL, *workers, *n, *month, *year)
	default:
		fmt.Fprintf(os.Stderr, "unknown -test %q (want login|transaction|monthly)\n", *scenario)
		os.Exit(1)
	}

	s := summarize(*scenario, *workers, *n, *instances, *baseURL, latencies, errCount, wall)
	s.printReport()

	if err := appendCSV(*csvPath, s); err != nil {
		fmt.Fprintf(os.Stderr, "failed to append CSV row to %s: %v\n", *csvPath, err)
		os.Exit(1)
	}
	fmt.Printf("  appended summary row to %s\n", *csvPath)
}
