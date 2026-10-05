# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@~/.claude/rules/engineering.md

## What this is

A CSE512 (distributed systems) course project: a mock banking app ("DISBank") whose
point is to demonstrate horizontal scaling. A stateless Go HTTP API is run as multiple
identical instances behind a sharded MongoDB cluster, and a load-testing harness measures
response times as you add server instances and shards. Read `SETUP.md` (environment +
cluster bring-up) and `CODE.md` (run order) before touching the runtime pieces — they
contain operational steps not encoded anywhere in the source.

## Architecture

Three independent tiers, each started by hand:

- **Backend** (`main.go`, `handlers/`, `db/`, `datamodels/`) — Go, module `cse512`, using
  `gorilla/mux` and the official `mongo-driver`. Each process is stateless and takes a
  `-p <port>` flag; horizontal scaling = run N copies on different ports (the README and
  load tester assume ports 8080–8085). There is no shared state between instances; all
  coordination happens in MongoDB.
- **Database** — a MongoDB **sharded cluster** stood up entirely from `scripts/` as Docker
  containers (`mongo:4.4`): 3 config servers, 3 `mongos` routers (host ports
  **27151/27152/27153**), and 3 shard replica sets of 3 nodes each. `db/db.go` hardcodes a
  connection to all three routers and reads from secondaries
  (`readpref.Secondary()`, `readconcern.Local()`). Database is `bank`; collections are
  `users` and `transactions`.
- **Frontend** (`UserInterface/`) — static `index.html` / `styles.css` / `app.js`, served
  by `http-server`. `app.js` hardcodes the backend at `http://localhost:8080`
  (`allowedPorts = [8080]`); change that array to point the UI at other instances.

### Things that will bite you

- **`PerformTransaction` requires the real sharded cluster.** It uses MongoDB
  multi-document sessions/transactions (`StartSession` → `StartTransaction` → `CommitTransaction`
  in `handlers/handletransaction.go`). These do not work against a standalone `mongod` — you
  must bring up the replica-set cluster via `scripts/main.sh`.
- **Self-transactions are encoded by `sender_id == receiver_id`**: deposit (positive amount)
  vs. withdrawal (negative amount). A normal transfer is `sender_id != receiver_id`. The same
  convention is baked into `generate_mock_transactions.js`.
- **Data load and indexing are manual.** There is no migration/seed step in code. Per
  `SETUP.md`, JSON dumps are imported through MongoDB Compass, and you must hand-create
  indices: ascending `sender_id` and `receiver_id` on `transactions`, and `user_id` on
  `users`. Queries will be slow or appear broken without them.
- **No auth/session tokens.** `/login` validates credentials and returns user data; there is
  no token, and subsequent endpoints trust the `sender_id`/`user_id` passed by the client.
- **No prebuilt binary is committed.** Build from source with `go build -o server main.go` (or
  `go run main.go`) for your platform — see below. (`*.exe` and `/server` are git-ignored.)

## Common commands

All Go commands run from the repo root (module `cse512`).

```bash
# Build a backend binary for the current platform
go build -o server main.go

# Run a backend instance (repeat on 8080..8085 for the load test)
./server -p 8080
# or without building:
go run main.go -p 8080

# Resolve dependency issues (per SETUP.md)
go mod vendor && go mod tidy
```

Cluster lifecycle (run from `scripts/`, scripts must be executable):

```bash
./main.sh              # full bring-up: network → config servers → shards → routers → connect
./stop_containers.sh   # stop all 15 containers
./start_containers.sh  # restart existing containers
./delete_containers.sh # tear down
```

Frontend (from `UserInterface/`):

```bash
http-server .          # serves the UI; open the printed URL
```

## Tests

`testing/*_test.go` are **integration tests, not unit tests** — they issue real HTTP
requests to `http://localhost:8080`, so a backend instance and a populated cluster must
already be running.

```bash
go test ./testing/...                 # run all integration tests
go test ./testing/ -run TestLogin -v  # run a single test
```

## Load / response-time harness

`responsetime/main.go` fires concurrent requests at instances on 8080–8085 and prints
latency/throughput. It has three scenarios — `testLogin`, `testTransaction`,
`testMonthlyTransactions` — and you **select which one runs by editing `main()`** (only the
uncommented call executes). Adjust `concurrentWorkers` / `totalRequests` in the test
function. Requires all six backend instances up.

```bash
go run responsetime/main.go
```

## Mock data generation

`generate_mock_users.js` and `generate_mock_transactions.js` (Node, `@faker-js/faker`)
produce the JSON dumps that are then imported into Mongo via Compass. `generate_mock_transactions.js`
reads `mock_data_userInfo.json` and writes `mock_transactions.json`; tune `numTransactions`
at the bottom of the file. The generated JSON files are git-ignored.

```bash
npm install                          # installs @faker-js/faker
node generate_mock_users.js
node generate_mock_transactions.js
```
