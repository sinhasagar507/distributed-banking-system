# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@~/.claude/rules/engineering.md

## What this is

A CSE512 (distributed systems) course project: a mock banking app ("DISBank") whose
point is to demonstrate horizontal scaling. A stateless Go HTTP API is run as multiple
identical instances behind a sharded, replicated MongoDB cluster, fronted by nginx, with
Prometheus/Grafana observability and a CLI load-testing harness that measures response
times as you add app instances. The whole stack comes up with one `docker compose up`.
`IMPROVEMENT_PLAN.md` has the full history of how it got here; `README.md` has the
architecture diagram and quickstart. See the "Legacy/manual path" section at the end of
this file for the pre-compose hand-built workflow, which still works but isn't how this
project is normally run anymore.

## Architecture

- **Backend** (`main.go`, `handlers/`, `internal/{config,store,service,auth,httpx,
  logging,metrics}`, `db/`, `datamodels/`) — Go, module `cse512`, using `gorilla/mux` and
  the official `mongo-driver`. Handlers are thin (parse → call a `service.Service`
  method → encode); `internal/store` is the only package that imports the mongo driver
  directly. Each process is stateless; all coordination happens in MongoDB. Configuration
  is env-driven (`internal/config`): `MONGO_URI`, `MONGO_READ_PREF`, `PORT`, `JWT_SECRET`,
  etc., each with a sane local-dev default.
- **Auth** (`internal/auth`) — HS256 JWT, 24h TTL. `/login` is the only unauthenticated
  route and returns a token; `/transaction`, `/transactions`, and `/monthdata` require
  `Authorization: Bearer <token>` and always act as the token's user — client-supplied
  `sender_id`/`user_id` fields are ignored.
- **Database** — a MongoDB **sharded cluster**: 3 config servers, 3 `mongos` routers, and
  3 shard replica sets of 3 nodes each, all defined in `docker-compose.yml` and brought up
  idempotently by `scripts/init/init-cluster.sh` (replica-set initiation, `addShard`,
  `enableSharding`, hashed-key `shardCollection`, and the indices that used to be manual
  Compass steps). Database is `bank`; collections are `users` and `transactions`. History
  reads use secondaries (`readpref.Secondary()`, local read concern) for throughput;
  balance-critical reads/writes use the primary with majority concern and run inside a
  real multi-document transaction (`store.RunInTransaction`, wrapping
  `session.WithTransaction`). Debits use a conditional `$inc`
  (`current_balance >= amount`) instead of read-then-write, so there's no TOCTOU balance
  race. Money is stored as integer cents everywhere (API, DB, CSV export).
- **App tier & load balancing** — 3 identical app instances (`app-1/2/3` in
  `docker-compose.yml`) behind `nginx` (`nginx/nginx.conf`), which round-robins across
  them. The single published entrypoint is `http://localhost:8080`; scaling out means
  adding more `app-N` services + nginx upstream entries.
- **Observability** — `/health` (liveness) and `/ready` (pings Mongo) are unauthenticated.
  `/metrics` (Prometheus format: a request-duration histogram + a request counter, both
  labeled by route/method/status) is also unauthenticated and excluded from its own
  instrumentation. `prometheus/prometheus.yml` scrapes each app instance directly (not
  through nginx) so per-instance behavior stays visible; Grafana
  (`grafana/provisioning/`, `grafana/dashboards/disbank.json`) ships a pre-provisioned
  dashboard. All logging is structured via `internal/logging` (wraps `log/slog`); the
  server sets explicit `http.Server` timeouts and shuts down gracefully on
  SIGINT/SIGTERM.
- **Frontend** (`UserInterface/`) — static `index.html`/`styles.css`/`app.js`, served by
  `http-server`. `app.js` hardcodes the backend at `http://localhost:8080`
  (`allowedPorts = [8080]`); sends the bearer token on every protected request; converts
  cents↔dollars at the API boundary.

### Things that will bite you

- **`PerformTransaction` requires the real sharded cluster.** It uses MongoDB
  multi-document transactions (`internal/store.RunInTransaction` in
  `internal/service/service.go`). These do not work against a standalone `mongod` — bring
  up the replica-set cluster via `docker compose up` (or `scripts/main.sh` for the legacy
  manual path).
- **Self-transactions are encoded by `sender_id == receiver_id`**: deposit (positive
  amount) vs. withdrawal (negative amount). A normal transfer is `sender_id !=
  receiver_id`. The same convention is baked into `scripts/seed/generate.js`.
- **No token = no access.** Every endpoint except `/login`, `/health`, `/ready`, and
  `/metrics` requires a valid bearer token; the acting user always comes from the token,
  never from the request body/query.
- **Seed data and indices are no longer manual.** `scripts/init/init-cluster.sh` creates
  everything (sharding + indices) idempotently as part of `docker compose up`; see "Mock
  data generation" below for regenerating the dataset itself.
- **No prebuilt binary is committed.** Build from source with `go build -o server main.go`
  (or `go run main.go`) for your platform. (`*.exe` and `/server` are git-ignored.) In
  practice you don't need a local binary at all — `docker compose up` builds the image.

## Common commands

`make` wraps the flows below (see `Makefile`): `make up`, `make seed`, `make test`, `make
bench`, `make build`, `make vet`, `make fmt`, `make frontend`.

```bash
# Bring up the full stack (cluster + seeding + 3 app instances + nginx + prometheus/grafana)
docker compose up -d --build

# Build/run a single backend binary outside docker (for local iteration)
go build -o server main.go
./server -p 8080
# or: go run main.go -p 8080

# Tear down
docker compose down -v
```

Legacy hand-built cluster lifecycle (run from `scripts/`, scripts must be executable) is
in the "Legacy/manual path" section at the end of this file.

Frontend (from the repo root):

```bash
npx http-server UserInterface/   # serves the UI; open the printed URL
```

## Tests

`go test ./...` runs everything:

- `internal/auth`, `internal/service`, `internal/metrics` — **unit tests**, no DB needed
  (the service layer is tested against `internal/store/storetest.FakeStore`, an in-memory
  fake with error injection).
- `testing/*_test.go` — **integration tests**, issue real HTTP requests against
  `API_BASE_URL` (defaults to `http://localhost:8080`), so the full compose stack must
  already be up. Includes `concurrency_test.go`, which fires concurrent transfers and
  asserts conservation of money.

```bash
go test ./... -count=1                          # everything
go test ./internal/... -count=1                 # unit tests only, no DB needed
API_BASE_URL=http://localhost:8080 go test ./testing/... -v   # integration tests, verbose
```

CI (`.github/workflows/ci.yml`) runs lint/vet, unit tests, and a full
`docker compose up` + integration-test job on every push.

## Load / response-time harness

`responsetime/main.go` is a CLI tool (flags, not edit-`main()`): `-test=login|transaction|
monthly`, `-workers`, `-n`, `-base-url` (defaults to nginx's published entrypoint,
`http://localhost:8080`), `-month`/`-year` (monthly scenario), `-instances` (informational,
recorded in the CSV for sweep charting), `-csv` (output path, default
`benchmarks/results.csv`). Each worker collects its own latency slice (no shared-counter
data race); the tool reports p50/p95/p99 (not just mean) and appends one CSV row per run.
`transaction`/`monthly` scenarios log in and carry a JWT automatically.

```bash
go run responsetime/main.go -test=login -workers=20 -n=500
go run responsetime/main.go -test=transaction -workers=20 -n=500 -instances=3
```

`benchmarks/chart.go` renders `benchmarks/scaling.svg` from the accumulated CSV (`go run
benchmarks/chart.go` — check the file for its own flags/assumptions).

## Mock data generation

`scripts/seed/generate.js` (Node, `@faker-js/faker` + `bcryptjs`) deterministically
generates `users.json`/`transactions.json` for `mongoimport` plus a `credentials.json`
side file (plaintext, for test/dev use only), bcrypt-hashing passwords at generation time.
`scripts/seed/sample/` is a small, committed fixture set (30 users/150 transactions, seed
512) that `scripts/init/init-cluster.sh` imports automatically on `docker compose up`.

```bash
npm install
node scripts/seed/generate.js                 # default: 5000 users / 50000 transactions
node scripts/seed/generate.js --users 30 --transactions 150 --seed 512 --out scripts/seed/sample
```

---

## Legacy / manual path

Before Phase 0 of `IMPROVEMENT_PLAN.md`, this project was run by hand, three independent
tiers started separately:

- Backend instances were started one at a time with `-p <port>` on ports 8080–8085, with
  no shared config — `db/db.go` hardcoded the router URIs.
- The cluster was brought up with `scripts/*.sh` directly (`./main.sh` for full bring-up,
  `./stop_containers.sh`/`./start_containers.sh`/`./delete_containers.sh` for lifecycle),
  connecting to routers on host ports 27151/27152/27153.
- Data load and indexing were manual: JSON dumps generated by (now-deleted)
  `generate_mock_users.js`/`generate_mock_transactions.js` were imported through MongoDB
  Compass, and indices (`sender_id`/`receiver_id` on `transactions`, `user_id` on `users`)
  were hand-created.
- There was no auth — `/login` returned user data with no token, and every endpoint
  trusted whatever `sender_id`/`user_id` the client sent.

`SETUP.md`/`CODE.md` still document this path end-to-end if you want to understand or
reproduce the cluster bring-up by hand rather than through `docker-compose.yml`. It still
works, but `docker compose up` is the maintained path going forward.
