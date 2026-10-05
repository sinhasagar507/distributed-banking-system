# DISBank — Improvement Plan

A staged plan to harden this CSE512 distributed-systems project into a portfolio piece
that reads as production-minded engineering. The existing sharded + replicated architecture
is the centerpiece — this plan makes it **reproducible, correct, secure, observable, and
benchmarked**, not simpler.

**Status legend:** `[ ]` not started · `[~]` in progress · `[x]` done

## How to read this document

Every suggested change below is documented with two blocks **before any code is written**:

1. **What exists today (snapshot)** — current behavior in plain English; the data flow
   (where data enters, transforms, exits); existing contracts (API signatures, DB schema,
   payloads downstream depends on); and known edge cases / fragile assumptions already baked in.
2. **What we're changing and why** — the problem statement (the *why*, not just the *what*);
   scope boundaries (what is explicitly out of scope); design decisions and the alternatives
   rejected (the "why not X"); and any migrations needed (schema, data, config).

## Guiding constraints
- Don't break the sharding story — it's the project's value. Make it reproducible, not simpler.
- Each phase ends in a working, committable state.
- Self-contained — the stack must run end-to-end without the 1.5M-row external dataset
  download (generate a small dataset at seed time).

## Open decisions (resolve before we begin building)
- [x] **Self-contained dataset** — commit a small generated dataset so the stack runs
  without the external Google-Drive download. *(Resolved yes — done in 0.5.)*
- [ ] **Frontend** — keep vanilla JS with light polish vs. migrate to React/Vite.
  *(Recommended: keep vanilla — the strength is the backend/distributed-systems story.)*
- [x] **Starting scope** — Phase 0 → 4 in order vs. a subset.
  *(Resolved: Phase 0 → 4 in order, as currently underway.)*
- [x] **Money representation** — keep integer whole-dollar amounts vs. migrate to integer
  cents (minor units). *(Resolved: minor units — done in 1.2.)*

---

## System snapshot (shared context for every change)

Three hand-started tiers, no shared state except MongoDB:

- **Backend** — Go module `cse512`, `gorilla/mux` + official `mongo-driver`. `main.go`
  registers four routes and serves them with `http.ListenAndServe` on a `-p` port. Stateless;
  horizontal scale = run N copies on 8080–8085.
- **Database** — a real `mongo:4.4` **sharded cluster** stood up from `scripts/` as 15 Docker
  containers (3 config, 3×3 shards, 3 `mongos` routers on host ports 27151/27152/27153). `db/db.go`
  hardcodes a multi-router URI, reads from **secondaries** with **local** read concern.
  Database `bank`; collections `users` and `transactions`.
- **Frontend** — static `UserInterface/` served by `http-server`; `app.js` hardcodes
  `allowedPorts = [8080]`.

**DB schema in use today** (no enforced schema — these are the Go structs that read/write it):

- `users`: `user_id int`, `first_name string`, `last_name string`, `email string`,
  `current_balance int` (whole dollars), `password string` (bcrypt hash, despite the JSON tag
  being `"password"`), `account_number int64`.
- `transactions`: `transaction_id int`, `sender_id int`, `amount int` (negative ⇒ withdrawal),
  `receiver_id int`, `remarks string`, `dateTimeStamp int64` (Unix seconds), `status string`
  (`"completed"` in seed data, `"success"`/`"failed"` when written by the API).

**Encoded conventions downstream depends on:**
- `sender_id == receiver_id` ⇒ self-transaction: `amount > 0` deposit, `amount < 0` withdrawal.
  `sender_id != receiver_id` ⇒ transfer. Baked into both the API and `generate_mock_transactions.js`.
- The frontend infers transfer direction by **string-matching `remarks`** (`includes("Transfer")`
  && `includes("from <name>")`) because the transactions list endpoint omits `sender_id`/`receiver_id`.
- No auth tokens anywhere; endpoints trust client-supplied `sender_id`/`user_id`.

Required indices (created **by hand** in Compass per `SETUP.md`, not in code): ascending
`sender_id` and `receiver_id` on `transactions`, `user_id` on `users`.

---

# Phase 0 — Reproducibility & repo hygiene *(P0, foundation)*
**Goal:** `docker compose up` brings up the entire stack (cluster + N app instances +
load balancer + seeded data) with one command.

### 0.1 — Repo cleanup `[x]`

**What exists today (snapshot)**
- *Behavior:* A 13 MB prebuilt `server.exe` (a macOS arm64 Mach-O despite the `.exe` name) is
  committed at the repo root and is the binary `SETUP.md` tells users to run. `.gitignore`
  tracks only `node_modules/` and the three generated `mock_*` JSON files. There is no
  `.dockerignore`. `vendor/` may or may not be committed depending on `go mod vendor` runs.
- *Data flow:* none — these are build artifacts and ignore rules, not runtime data.
- *Contracts:* `SETUP.md` §4 references `./server.exe -p 8080` as the run command; the load
  harness and README assume a built binary exists. Removing it changes the documented run step.
- *Edge cases / fragile assumptions:* the committed binary can silently mismatch the user's
  platform or lag the source (CLAUDE.md already warns about this). A stale binary "works" but
  doesn't reflect the latest handlers — a real footgun for reviewers.

**What we're changing and why**
- *Problem:* committed binaries bloat the repo, drift from source, and signal non-production
  hygiene to anyone reviewing this as a portfolio piece.
- *Change:* remove `server.exe`; add `*.exe`, `/server`, `/vendor/`, `/benchmarks/raw/`,
  `.DS_Store` to `.gitignore`; add a `.dockerignore` (exclude `.git`, `node_modules`, `vendor`,
  generated data, binaries) so image builds stay small and deterministic.
- *Out of scope:* the actual Dockerfile/compose (0.3/0.4); CI (Phase 2).
- *Design decisions / rejected alternatives:* **rejected** keeping the binary "for convenience" —
  `go build` / `go run` is the right path and is already documented. **Rejected** committing
  `vendor/` — modules + `go.sum` already pin versions; vendoring is only worth it if CI needs
  network isolation, which we'll revisit if Actions flakes.
- *Migrations:* update `SETUP.md`/`CODE.md` run commands from `./server.exe` to `go run`/compose.
  No schema/data migration.

### 0.2 — Config externalization `[x]`

**What exists today (snapshot)**
- *Behavior:* `db/db.go` hardcodes `mongodb://localhost:27151,localhost:27152,localhost:27153`,
  `SetMaxPoolSize(30000)`, `SetMinPoolSize(10)`, `SetMaxConnIdleTime(5m)`,
  `SetReadPreference(Secondary())`, `SetReadConcern(Local())`. `main.go` reads only the `-p`
  flag (no env fallback). The client is a package-level singleton built lazily by `GetClient()`.
- *Data flow:* config is compile-time constant → `connect()` → `mongo.Connect` → cached `client`.
- *Contracts:* anything importing `db.GetClient()` expects a ready `*mongo.Client`. Pool size
  and read preference are invisible global tuning knobs today.
- *Edge cases / fragile assumptions:* (a) `GetClient()`'s `if client == nil` check is **not
  synchronized** — concurrent first-callers can race and dial twice. (b) `MaxPoolSize(30000)`
  is enormous and untunable without a recompile. (c) the URI only works on a host that has the
  routers on `localhost:2715x`; inside Docker it must be service names — impossible to change
  without editing source.

**What we're changing and why**
- *Problem:* every deployment knob is hardcoded, so the same binary can't run on a laptop and in
  a container, and the connection pool can't be tuned for benchmarks without a rebuild.
- *Change:* add `internal/config/config.go` that reads `MONGO_URI`, `MONGO_MAX_POOL`,
  `MONGO_MIN_POOL`, `MONGO_READ_PREF`, `PORT`, `JWT_SECRET` from env with sane defaults
  (defaults preserve today's local behavior). Rewrite `db/db.go` to consume config and to build
  the client behind a `sync.Once`. `main.go` keeps `-p` but falls back to `PORT`.
- *Out of scope:* multi-region / TLS / auth-enabled Mongo connection strings; secret managers.
- *Design decisions / rejected alternatives:* **env vars over a config file** — 12-factor, plays
  natively with compose/CI, no parser dependency. **Rejected** Viper/koanf — overkill for ~6 keys.
  `sync.Once` over a mutex'd nil-check — simplest correct lazy-init.
- *Migrations:* **config migration** only — document the new env vars; defaults mean existing
  local runs are unaffected. No schema/data change.

### 0.3 — App containerization `[x]`

**What exists today (snapshot)**
- *Behavior:* no Dockerfile. The app runs as a host process via `go run`/`server.exe`. Only the
  *database* is containerized (`scripts/`).
- *Data flow:* n/a.
- *Contracts:* the app listens on `:$PORT` and dials Mongo at the (currently hardcoded) URI.
  A container must expose the port and reach the routers by **service name**, not `localhost`.
- *Edge cases / fragile assumptions:* CGO is off by default for this pure-Go app, so a static
  build is straightforward; the `localhost` URI (0.2) is the blocker to containerizing today.

**What we're changing and why**
- *Problem:* without an app image there is no one-command stack and no CI parity between local
  and pipeline.
- *Change:* add a multi-stage `Dockerfile` — `golang:1.23` builder (`CGO_ENABLED=0 go build`) →
  minimal `distroless/static` or `alpine` runtime image; non-root user; `EXPOSE` the port;
  entrypoint runs the server reading `PORT`/`MONGO_URI` from env.
- *Out of scope:* the orchestration of N replicas + LB (0.4); image publishing to a registry.
- *Design decisions / rejected alternatives:* **distroless/static** for a tiny, CVE-light image
  (depends on the static build from 0.2). **Rejected** a fat `golang` runtime image — 800 MB+
  for no reason. Multi-stage keeps the final image to the binary only.
- *Migrations:* depends on **0.2** (env-driven Mongo URI) landing first.

### 0.4 — Full-stack `docker-compose.yml` + init container `[x]`

**What exists today (snapshot)**
- *Behavior:* the cluster is brought up by a hand-run chain of shell scripts
  (`scripts/main.sh` → `create_network.sh` → `config_servers.sh` → `create_shards.sh` →
  `create_routers.sh` → `connect_shards.sh`), each `docker run`-ing `mongo:4.4` containers and
  `docker exec`-ing `rs.initiate` / `sh.addShard` style commands. Replica-set init, shard
  enablement, **collection sharding**, and **index creation** are all manual (the last via
  Compass, per `SETUP.md`). The app and frontend are started separately by hand.
- *Data flow:* operator → shell scripts → 15 containers; operator → Compass → data + indices.
- *Contracts:* host ports 27151/27152/27153 for routers; database `bank`; collections `users`,
  `transactions`. The app's hardcoded URI depends on those exact host ports.
- *Edge cases / fragile assumptions:* the scripts have **no healthchecks/readiness gating** —
  ordering is enforced only by `set -e` and run-time luck; on a cold machine `connect_shards.sh`
  can fire before a replica set has elected a primary. Index creation being manual means queries
  silently table-scan if a reviewer skips that step. Client-picks-a-random-port (`allowedPorts`,
  load harness `counter % 6`) is a stand-in for a load balancer.

**What we're changing and why**
- *Problem:* the stack is irreproducible: ~6 scripts + Compass clicks + manual indices + manual
  server starts. A reviewer can't `up` it, and CI can't run it.
- *Change:* a single `docker-compose.yml` that declares the 15 Mongo containers (with
  healthchecks) **translated from `scripts/`**, plus a one-shot `mongo-init` service that runs
  `rs.initiate`, `sh.addShard`, `enableSharding`, `shardCollection`, **and index creation**
  (`sender_id`/`receiver_id` on `transactions`, `user_id` on `users`) gated on healthchecks —
  replacing the Compass steps. Then 3 app instances + an **nginx** load balancer in front
  (replacing the client-side random-port hack).
- *Out of scope:* Kubernetes/Swarm; production-grade Mongo auth/TLS; autoscaling.
- *Design decisions / rejected alternatives:* **keep the full sharded topology** — it's the
  project's whole point; we make it reproducible, not smaller. **Init container over baking init
  into each Mongo container** — keeps the cluster definition declarative and idempotent.
  **nginx over the random-port client hack** — gives a single stable entrypoint and a real,
  explainable LB tier. **Rejected** collapsing to one `mongos`/one shard for simplicity — that
  would erase the scaling story.
- *Migrations:* **config** (service-name URIs via 0.2), **infra** (scripts → compose).
  `scripts/` retained as the documented "how the cluster is built from primitives" reference.
  The manual index step is migrated into code (init container).

### 0.5 — Automated, deterministic seeding `[x]`

**What exists today (snapshot)**
- *Behavior:* `generate_mock_users.js` (Node + `@faker-js/faker`) writes
  `mock_data_userInfo.json` (3000 users; **plaintext** passwords). `generate_mock_transactions.js`
  reads that file and writes `mock_transactions.json` (25k transactions). Real demo data is a
  **1.5M-row external download** from Google Drive, imported via Compass.
- *Data flow:* faker → JSON files → (manual) Compass import → Mongo.
- *Contracts:* the generators encode the same `sender_id==receiver_id` self-txn convention and
  the negative-amount-for-withdrawal rule the API relies on. `user_id` starts at 100 and
  auto-increments; `account_number` is a unique 9-digit int.
- *Edge cases / fragile assumptions:* **the seed/auth mismatch** — the generator emits plaintext
  passwords, but `handlers/login.go` verifies with `bcrypt.CompareHashAndPassword`. Logins only
  work because a *separate, uncommitted* hashing step produces `mock_data_userInfo_pwd.json`
  (referenced only in `.gitignore`). So seeding is not reproducible from the repo as-is. Faker
  output is also **non-deterministic** (no seed), so test fixtures (`responsetime/main.go`'s
  hardcoded emails/passwords, the integration tests' user 106) can't be regenerated.

**What we're changing and why**
- *Problem:* the stack can't run from a clean clone — it needs an external 1.5M-row download and
  an uncommitted password-hashing step, and the data isn't reproducible.
- *Change:* refactor the generators into `scripts/seed/` to (a) seed faker deterministically,
  (b) generate a small ~5k-user / ~50k-txn dataset, (c) **bcrypt-hash passwords as part of
  generation** (closing the mismatch), and (d) emit files the `mongo-init` container loads via
  `mongoimport`. Commit a tiny sample dataset for CI/reviewers.
- *Out of scope:* keeping/serving the 1.5M-row dataset (becomes optional, documented separately);
  realistic transaction graphs/fraud patterns.
- *Design decisions / rejected alternatives:* **deterministic seed** so fixtures and benchmarks
  are reproducible. **Hash at generation time** so there is one source of truth for credentials.
  **Commit a small sample** so CI doesn't depend on faker or external downloads. **Rejected**
  keeping Compass import in the loop — manual, unrepeatable. **Rejected** generating millions of
  rows in CI — slow; the scaling story is shown by the benchmark sweep (Phase 3), not row count.
- *Migrations:* **data migration** — regenerate with hashed passwords; existing test fixtures
  (emails/passwords/user ids in `responsetime/main.go` and `testing/`) must be regenerated to
  match the new deterministic dataset. Document the optional large-dataset path.

**Phase 0 outcome:** one command yields a live, seeded, sharded system.

---

# Phase 1 — Correctness & security *(P0, the credibility batch)*
**Goal:** It behaves like a bank, not a prototype. Produces the best interview stories.

### 1.1 — Refactor for testability (service + store layers) `[x]`

**What exists today (snapshot)**
- *Behavior:* all business logic lives **inside the HTTP handlers**. `PerformTransaction`
  ([handlers/handletransaction.go](handlers/handletransaction.go)) is ~270 lines mixing request
  parsing, validation, balance reads, Mongo session/transaction orchestration, error logging, and
  response encoding. The other three handlers follow the same shape.
- *Data flow:* `*http.Request` → inline decode → inline `db.GetClient()` calls → inline encode.
- *Contracts:* handlers are coupled directly to `db.GetClient()` and the `mongo` driver types;
  there is no seam to substitute a fake DB.
- *Edge cases / fragile assumptions:* because logic and transport are fused, **nothing is unit
  testable** without a live cluster (today's `testing/*` are integration tests against `:8080`).
  Money-movement rules can't be exercised in isolation.

**What we're changing and why**
- *Problem:* untestable, un-mockable handlers block the concurrency proof (Phase 2) and make the
  race fix (1.2) hard to verify in isolation.
- *Change:* extract `internal/service/` (money movement, txn-type classification, balance rules)
  and `internal/store/` (Mongo read/write behind interfaces). Handlers become thin: parse →
  call service → encode. Service depends on a `Store` interface, not the driver.
- *Out of scope:* changing API request/response shapes (kept identical so the frontend and load
  harness keep working); switching off MongoDB.
- *Design decisions / rejected alternatives:* **interface seam at the store** so the service is
  unit-testable with a fake. **Rejected** a full hexagonal/DDD layering — too heavy for four
  endpoints. **Rejected** an ORM — the driver is fine and the sharding behavior must stay visible.
- *Migrations:* none (pure refactor); behavior must be byte-for-byte preserved, guarded by the
  existing integration tests before/after.

### 1.2 — Fix the transfer: real atomicity + the balance race + money units `[x]`

> This is the centerpiece correctness fix. The current plan framed it only as a TOCTOU race;
> the code review surfaced a **more severe, present bug** that must be fixed in the same change.

**What exists today (snapshot)**
- *Behavior (intended):* `PerformTransaction` is supposed to move money atomically inside a
  MongoDB multi-document transaction (`StartSession` → `StartTransaction` → updates →
  `CommitTransaction`, with `AbortTransaction` on error).
- *Behavior (actual):* the balance updates are issued as
  `usersCollection.UpdateOne(context.Background(), …)` — i.e. with a **plain background context,
  not the session context**. In `mongo-driver` v1.x, operations only run inside a transaction
  when they receive the session's context (via `mongo.WithSession`/`session.WithTransaction`).
  So today the two `$inc` updates execute as **independent autocommit writes outside the
  transaction**; `StartTransaction`/`Commit`/`Abort` have **no effect** on them. Consequences:
  - **No atomicity.** For a transfer, the sender debit can succeed and the receiver credit fail
    (or the process die in between) leaving money destroyed/created. `AbortTransaction` on error
    does **not** roll back the already-applied `$inc`.
  - **TOCTOU race.** Balance is read at line ~108 (before any session) and the sufficiency check
    (`sender.Balance < amount`) happens there; the debit happens later. Concurrent transfers from
    the same account can both pass the check and overdraw.
  - **Stale success balance.** The post-"commit" re-read (`FindOne(... senderID ...)`) also uses
    background context **and** the client's default **`Secondary` read pref** — it can read a
    replica that hasn't received the write yet, so `updated_balance` returned to the UI can be the
    **pre-transfer** value.
  - **Self-withdrawal bypasses the funds check.** The guard is
    `if sender.Balance < amount && senderID != receiverID` — for a self-withdrawal
    (`senderID==receiverID`, `amount<0`) the check is skipped entirely, so `$inc` can drive a
    balance negative.
- *Data flow:* JSON body → struct → sender `FindOne` (secondary) → checks → receiver `FindOne`
  → account-number match → (ineffective) session → two `$inc` updates (background ctx) →
  insert a `transactions` row → stale re-read → response.
- *Contracts (must preserve):* request `{sender_id, receiver_id, account_number, amount, remarks,
  dateTimeStamp}`; response `{status, message, updated_balance}`; the `sender_id==receiver_id`
  deposit/withdraw convention; failed attempts logged into `transactions` with `status:"failed"`.
- *Edge cases / fragile assumptions:* `amount == 0` rejected; receiver `account_number` must
  match; on "sender not found" it logs a `"failed"` transaction (semantically odd — see 1.4);
  amounts are **whole-dollar ints**, and `monthdata` formats them as `$%.2f` of the int, so the
  unit is implicitly dollars with no sub-dollar precision.

**What we're changing and why**
- *Problem:* the money-movement path is not actually atomic, has a check-then-act overdraw race,
  can report stale balances, and lets self-withdrawals overdraw. For a "bank" this is the headline
  correctness gap (and the best interview story once fixed).
- *Change:*
  1. Run all writes **inside the session** via `session.WithTransaction(sc, fn)`, passing the
     session context `sc` to every `UpdateOne`/`InsertOne` so they are genuinely transactional.
  2. Replace check-then-act with a **conditional update inside the transaction**:
     `UpdateOne({user_id, current_balance: {$gte: amount}}, {$inc: {current_balance: -amount}})`
     and **abort if `MatchedCount == 0`** (insufficient funds) — atomic guard, no TOCTOU.
  3. Apply the same `$gte` guard to **self-withdrawals** so they can't overdraw.
  4. Use **primary read / majority concern** for balance-critical reads and the post-commit
     balance (keep secondary/local only for history reads). Return a balance read with majority
     concern (or derive it from the update result) so `updated_balance` is never stale.
  5. (Tied to the Open Decision) migrate `amount`/`current_balance` to **integer minor units
     (cents)** to remove the implicit-dollars ambiguity and the `$%.2f`-of-an-int formatting hack.
- *Out of scope:* multi-currency; double-entry ledger accounting; idempotency keys for retries
  (note as "what I'd do next").
- *Design decisions / rejected alternatives:* **conditional `$inc` over read-modify-write** —
  the canonical lock-free way to make balance updates safe and the crux of the interview narrative.
  **`session.WithTransaction` over manual Start/Commit** — it handles transient-error retries and
  makes the session-context binding impossible to forget (the exact bug today). **Majority concern
  for balances, secondary/local for history** — explicit consistency-vs-throughput trade-off per
  read. **Rejected** keeping secondary reads for balances "for speed" — that's what produces stale
  balances; correctness wins here.
- *Migrations:* **schema/data** if we adopt cents (multiply existing `amount`/`current_balance`
  by 100 in the seed + any imported data; update generators, `monthdata` formatting, and the UI
  `Intl.NumberFormat` divisor). **No API field renames.** Coordinate with 1.1 (logic now lives in
  the service) and 0.4/0.5 (init container re-seeds with the new units).

### 1.3 — Real authorization via JWT `[x]`

**What exists today (snapshot)**
- *Behavior:* `/login` ([handlers/login.go](handlers/login.go)) requires `user_id` **and**
  `email` **and** `password`, verifies the bcrypt hash and an exact email match, and returns user
  data (`user_id, email, name, balance, account_number`). **No token is issued.** Every other
  endpoint trusts a client-supplied identifier: `/transaction` and `/transactions` act on the
  `sender_id` in the request; `/monthdata` acts on the `user_id` query param.
- *Data flow:* login → JSON user object → frontend stores it in a JS variable → subsequent calls
  pass `sender_id`/`user_id` from that object.
- *Contracts:* login response `{status, message, data:{user_id, email, name, balance,
  account_number}}`; downstream endpoints' identity parameters are `sender_id` (body/query) and
  `user_id` (query).
- *Edge cases / fragile assumptions:* **anyone can move or read anyone's money** by passing a
  different `sender_id`/`user_id` — there is no session or ownership check. `strconv.Atoi(userID)`
  errors are ignored in login. `result["first_name"].(string)` is an **unchecked type assertion**
  that panics if the field is missing/non-string. Login uniquely requires all three of
  id+email+password to match — unusual but currently load-bearing for the test fixtures.

**What we're changing and why**
- *Problem:* the app has authentication but **no authorization** — the defining security hole for
  a banking app. The acting user must come from a verified token, not a client field.
- *Change:* add `internal/auth/` — `/login` issues a signed **JWT** (HS256, `JWT_SECRET` from
  config) carrying `user_id`; an auth middleware validates the token on `/transaction`,
  `/transactions`, `/monthdata` and injects the authenticated `user_id` into the request context.
  Handlers derive the acting user **from the token**, ignoring/validating any client-supplied id
  against it. Harden login: checked type assertions, handled `Atoi` errors.
- *Out of scope:* refresh tokens, token revocation/blocklist, OAuth, RBAC/roles, rate limiting
  (note as "what I'd do next").
- *Design decisions / rejected alternatives:* **stateless JWT over server-side sessions** — fits
  the stateless-horizontal-scaling thesis (no shared session store needed across the N instances).
  **HS256 with a shared secret** — simplest for a single backend service; note ES256/asymmetric as
  the multi-service upgrade. **Rejected** keeping client-supplied identity "because it's a demo" —
  it's the one security flaw a reviewer will immediately flag.
- *Migrations:* **config** (`JWT_SECRET`); **frontend** must store the token and send
  `Authorization: Bearer` (touches `app.js` login + the three authed fetches); **tests/harness**
  must log in first and carry the token (Phase 2 / change 3.3). No DB schema change.

### 1.4 — Shared HTTP middleware + honest error logging `[x]`

**What exists today (snapshot)**
- *Behavior:* every handler hand-writes the same four `Access-Control-Allow-*` headers, the same
  `OPTIONS` short-circuit, and the same method guard. CORS is `*`. `PerformTransaction`'s
  `insertErrorTransaction` writes a `status:"failed"` row into `transactions` even when the real
  cause is "sender not found" or a JSON parse failure.
- *Data flow:* per-handler boilerplate → response; error path → `transactions` insert.
- *Contracts:* CORS allows any origin; preflight returns 200; failed-attempt rows currently exist
  in `transactions` and the load/seed data may include them.
- *Edge cases / fragile assumptions:* the duplicated boilerplate drifts (each handler sets a
  slightly different `Allow-Methods`); logging a "failed transaction" for a *lookup* failure
  pollutes transaction history with non-transactions and is semantically wrong.
- *Edge cases / fragile assumptions:* error responses are ad-hoc (`http.Error` plaintext in
  `monthdata`, JSON structs elsewhere) — inconsistent shapes for the frontend.

**What we're changing and why**
- *Problem:* copy-pasted cross-cutting concerns drift and the error-logging semantics are wrong,
  polluting the `transactions` collection.
- *Change:* add `internal/httpx/` with one middleware chain for CORS/OPTIONS/method-guard/
  structured-error/request-id, applied via the mux router once. Stop writing a `"failed"`
  transaction on non-transaction errors (parse error, sender/receiver not found); only log a
  genuine failed *money movement*. Standardize the JSON error envelope across all endpoints.
- *Out of scope:* tightening CORS to a real allowlist is deferred to config (note it), since the
  demo serves the UI from an arbitrary `http-server` port.
- *Design decisions / rejected alternatives:* **one middleware over per-handler headers** — single
  source of truth, no drift. **Rejected** a heavy framework (chi/echo) — `gorilla/mux` already in
  use; a small middleware is enough and keeps the dependency surface flat.
- *Migrations:* none (behavioral/log-semantics change). Note: removing bogus `"failed"` rows from
  seed data is handled by the 0.5 regeneration.

**Phase 1 outcome:** correct concurrent money movement + real authorization.

---

# Phase 2 — Testing & CI *(P0/P1)*
**Goal:** Green CI; the race fix is proven, not asserted.

### 2.1 — Unit tests for the service layer `[x]`

**What exists today (snapshot)**
- *Behavior:* the only tests are `testing/login_test.go` and `testing/gettransactions_test.go` —
  **integration** tests that `http.Post`/`http.Get` against a live `http://localhost:8080` with a
  populated cluster. There are **zero unit tests**.
- *Data flow:* test → real HTTP → real backend → real Mongo.
- *Contracts:* `gettransactions_test.go` asserts exact amounts (`1452`, `-5969`) and
  `status:"completed"` for user 106 — i.e. it is hardcoded to the specific external dataset.
- *Edge cases / fragile assumptions:* tests fail/skip entirely without the full stack and the
  exact seed data; they can't run in CI as-is and don't isolate logic.

**What we're changing and why**
- *Problem:* core money math and auth have no fast, deterministic coverage; regressions in the
  race fix wouldn't be caught.
- *Change:* unit-test the `internal/service` and `internal/auth` packages against the fake
  `Store` (from 1.1): balance math, txn-type classification (deposit/withdraw/transfer),
  insufficient-funds rejection, self-withdrawal guard, JWT issue/verify (valid, expired, tampered).
- *Out of scope:* testing Mongo's transaction semantics in a unit test (that's the integration/
  concurrency job — 2.2/2.3).
- *Design decisions / rejected alternatives:* **fake store over a mocked driver** — tests the
  rules, not the driver. **Table-driven Go tests** — idiomatic, easy to extend.
- *Migrations:* depends on **1.1** seam existing.

### 2.2 — Concurrency test (conservation of money) `[x]`

**What exists today (snapshot)**
- *Behavior:* nothing exercises concurrent transfers; the TOCTOU/atomicity bugs (1.2) are
  currently invisible to the test suite.
- *Contracts:* n/a (new test).
- *Edge cases / fragile assumptions:* requires the **real cluster** because correct behavior
  depends on actual Mongo transactions (the fix's whole point).

**What we're changing and why**
- *Problem:* the headline fix (1.2) must be *proven* under concurrency, not asserted in prose.
- *Change:* an integration test that fires **N simultaneous transfers** against one account and
  asserts **conservation of money** (sum of balances constant; no overdraft below zero) and that
  exactly the expected number succeed/fail. Run it against the compose stack.
- *Out of scope:* chaos/fault injection (killing a shard mid-transfer) — note as a stretch.
- *Design decisions / rejected alternatives:* **conservation invariant over per-request asserts**
  — it's the property that actually distinguishes the fixed code from the broken code. Run
  pre-fix to watch it fail, post-fix to watch it pass (great for the writeup).
- *Migrations:* depends on **1.2** and the compose stack (0.4).

### 2.3 — Make integration tests environment-driven `[x]`

**What exists today (snapshot)**
- *Behavior:* `testing/*_test.go` hardcode `http://localhost:8080` and dataset-specific
  expectations (user 106, exact amounts, specific emails/passwords).
- *Contracts:* assertions are coupled to the external 1.5M-row dataset.
- *Edge cases / fragile assumptions:* cannot point at a compose stack or CI service; break if the
  dataset changes (which 0.5 will do).

**What we're changing and why**
- *Problem:* tests are tied to one machine and one dataset, so they can't run in CI.
- *Change:* read the base URL from an env var (e.g. `API_BASE_URL`, default `:8080`); re-point
  expectations at the **committed deterministic sample** (0.5). Add auth (log in, carry the JWT).
- *Out of scope:* rewriting them into unit tests (they remain true integration tests by design).
- *Design decisions / rejected alternatives:* **env-driven base URL** — minimal change, lets the
  same tests run locally and in CI/compose. **Rejected** mocking the server in these tests — their
  value is end-to-end coverage.
- *Migrations:* **data** (fixtures → deterministic sample, 0.5); **config** (`API_BASE_URL`);
  depends on **1.3** for the token flow.

### 2.4 — GitHub Actions CI `[x]`

**What exists today (snapshot)**
- *Behavior:* no CI, no lint config, no README badge. Quality is unverified on push.
- *Contracts:* n/a.
- *Edge cases / fragile assumptions:* without CI, the reproducibility work (Phase 0) is unproven
  on a clean machine.

**What we're changing and why**
- *Problem:* a portfolio repo needs a visible, automated proof it builds and passes.
- *Change:* `.github/workflows/ci.yml`: `golangci-lint` → `go vet` → unit tests (2.1) →
  `docker compose up` → integration + concurrency tests (2.2/2.3) → teardown. Add a status badge
  to the README.
- *Out of scope:* CD/deploys, image publishing, release automation.
- *Design decisions / rejected alternatives:* **compose-in-CI over a Mongo service container** —
  exercises the real sharded topology, which is the project's point. **Rejected** standalone
  `mongod` in CI — transactions/sharding wouldn't be representative.
- *Migrations:* depends on the **compose stack (0.4)** and **deterministic seed (0.5)**.

**Phase 2 outcome:** automated proof the system works under concurrency.

---

# Phase 3 — Observability & benchmarks *(P1, the differentiator)*
**Goal:** The scaling claim becomes a graph.

### 3.1 — Health, readiness & structured logging `[ ]`

**What exists today (snapshot)**
- *Behavior:* `main.go` calls `http.ListenAndServe` directly — **no timeouts, no graceful
  shutdown, no `/health` or `/ready`**. Logging is ad-hoc `fmt.Println` (e.g.
  "Failed transaction logged.").
- *Data flow:* logs → stdout as unstructured lines; no request correlation.
- *Contracts:* an LB/orchestrator has nothing to probe; nothing to scrape for liveness.
- *Edge cases / fragile assumptions:* no `ReadTimeout`/`WriteTimeout`/`IdleTimeout` ⇒ slowloris-
  style exposure and leaked connections under load; no `os/signal` handling ⇒ in-flight requests
  cut on shutdown.

**What we're changing and why**
- *Problem:* you can't operate, load-balance, or debug what you can't observe; abrupt shutdown
  corrupts in-flight load tests.
- *Change:* add `/health` (process up) and `/ready` (Mongo reachable) endpoints; replace
  `fmt.Println` with `slog` structured logging behind a **request-ID middleware**; configure an
  explicit `http.Server` with timeouts and `Shutdown(ctx)` on `SIGINT/SIGTERM`.
- *Out of scope:* distributed tracing/OpenTelemetry (note as next step).
- *Design decisions / rejected alternatives:* **stdlib `slog` over zap/zerolog** — no new dep,
  structured enough. **Separate `/health` vs `/ready`** — liveness shouldn't fail just because
  Mongo blips; readiness should. nginx/compose use `/ready` for gating.
- *Migrations:* **config** (log level/format); nginx healthcheck path (0.4). No schema/data change.

### 3.2 — Prometheus metrics (+ optional Grafana) `[ ]`

**What exists today (snapshot)**
- *Behavior:* the only performance signal is `responsetime/main.go`'s printed averages. No
  metrics endpoint, no histograms, no per-endpoint error counts.
- *Contracts:* n/a.
- *Edge cases / fragile assumptions:* the scaling claim is only as good as one ad-hoc run's
  average — no tail latency, no time series.

**What we're changing and why**
- *Problem:* "it scales" needs continuous, per-endpoint latency/error data, not a single average.
- *Change:* expose `/metrics` (Prometheus client) with request-latency **histograms** and error
  counters labeled by endpoint/status; add optional Prometheus + Grafana services to compose with
  a committed dashboard JSON.
- *Out of scope:* alerting, long-term storage, SLO definitions.
- *Design decisions / rejected alternatives:* **histograms over gauges/averages** — needed for
  p95/p99. **Optional** Grafana so the core stack stays lean; the dashboard JSON is committed so
  it's reproducible. **Rejected** push-gateway — scrape model fits the always-on app.
- *Migrations:* compose additions (0.4). No schema/data change.

### 3.3 — Benchmark harness rework `[ ]`

**What exists today (snapshot)**
- *Behavior:* `responsetime/main.go` has three scenarios (`testLogin`, `testTransaction`,
  `testMonthlyTransactions`); **you choose one by editing `main()`** (only the uncommented call
  runs). It spreads requests over 8080–8085 via `counter % 6`, prints total time, **average**
  latency, RPS, and a failure count.
- *Data flow:* hardcoded payloads → goroutine pool → per-request durations on a channel → summed.
- *Contracts:* assumes all six instances up; login payloads are hardcoded to specific dataset
  users; `getMonthlyTransactions` picks random user/month/year.
- *Edge cases / fragile assumptions:* the shared `counter` is **mutated by all worker goroutines
  without synchronization** (a data race that also skews server distribution); `rand.Seed` is
  called *inside* the per-iteration loop; **average only** (no percentiles), so tail latency — the
  interesting part of a scaling story — is invisible; `avgDuration = totalDuration/totalRequests`
  divides by the planned count even when requests failed, skewing the number; results go to stdout
  only (no machine-readable output to chart).

**What we're changing and why**
- *Problem:* the harness can't produce the tail-latency-vs-scale evidence the project's thesis
  needs, and it has a data race that undermines its own numbers.
- *Change:* replace edit-`main()` with **CLI flags** (`-test transaction -workers 100 -n 1000
  -targets http://...`); fix the `counter` race (atomic or per-worker counters) and move
  `rand.Seed` out of the loop; compute **p50/p95/p99** alongside average and true RPS over
  *completed* requests; write **CSV** to `/benchmarks` for charting; drive traffic through the
  nginx LB by default.
- *Out of scope:* a full load-testing framework (k6/Locust) — keep it Go and self-contained, but
  note them as alternatives.
- *Design decisions / rejected alternatives:* **percentiles over average** — averages hide the
  tail that distinguishes a scaled system. **CSV output** so the charts are reproducible from
  committed data. **Flags over editing `main()`** — scriptable sweeps. **Rejected** swapping to k6
  — the Go harness is part of the story and keeps the repo dependency-light.
- *Migrations:* fixtures must match the deterministic dataset (0.5); target LB instead of raw
  ports (0.4). With JWT (1.3), the transaction scenario must authenticate first.

### 3.4 — Run the sweep & commit results `[ ]`

**What exists today (snapshot)**
- *Behavior:* no committed benchmark results; the scaling claim is verbal.
- *Contracts:* n/a.
- *Edge cases / fragile assumptions:* results would be non-reproducible without the deterministic
  dataset and a stable LB entrypoint (both delivered earlier in the plan).

**What we're changing and why**
- *Problem:* the headline claim ("throughput scales ~linearly 1→6 instances") has no evidence
  attached.
- *Change:* sweep **1→6 app instances** and **1→3 shards**, commit the CSVs and generated charts
  (throughput & tail-latency vs. scale) under `/benchmarks`, and embed them in the README.
- *Out of scope:* cloud/multi-host benchmarking — single-host Docker is the documented harness.
- *Design decisions / rejected alternatives:* **commit raw CSV + chart** so claims are auditable.
  **Vary instances and shards independently** to separate app-tier from data-tier scaling.
- *Migrations:* depends on **3.3** (CSV output) and **0.4** (scalable compose).

**Phase 3 outcome:** "Throughput scaled ~linearly 1→6 instances," with the chart to back it.

---

# Phase 4 — Docs & demo *(P0/P1)*
**Goal:** Strong first impression in 30 seconds.

### 4.1 — README rewrite `[ ]`

**What exists today (snapshot)**
- *Behavior:* `README.md` is 4 lines (a GitHub link + "read SETUP.md then CODE.md"). The real
  instructions live in `SETUP.md`/`CODE.md` and describe the **manual** flow (Compass import,
  hand-created indices, `server.exe`).
- *Contracts:* the README is the entry point reviewers hit first.
- *Edge cases / fragile assumptions:* it documents a process that this plan is replacing; left
  unchanged it would contradict the new compose flow.

**What we're changing and why**
- *Problem:* the front door doesn't sell the architecture or show how to run it in one command.
- *Change:* rewrite with a **Mermaid architecture diagram** (client → nginx → app → mongos →
  shard → replica set), a one-command quickstart, the benchmark graphs (3.4), a **trade-offs
  section** (hashed shard keys → even distribution but `sender_id` lookups scatter-gather;
  secondary reads → throughput vs. staleness; conditional `$inc` → lock-free safety), and a
  "what I'd do next" section.
- *Out of scope:* a docs site; API reference generation.
- *Design decisions / rejected alternatives:* **lead with the diagram + trade-offs** — that's the
  distributed-systems signal a reviewer wants. **Rejected** leaving deep ops detail in the README
  — keep it skimmable, link to SETUP/CODE for depth.
- *Migrations:* must land **after** the compose flow (0.4) and benchmarks (3.4) so it documents
  reality, not aspiration.

### 4.2 — Makefile `[ ]`

**What exists today (snapshot)**
- *Behavior:* no Makefile; commands are scattered across CLAUDE.md/SETUP.md/CODE.md
  (`go build`, `go run`, `scripts/*.sh`, `http-server`, `go test`).
- *Contracts:* n/a.
- *Edge cases / fragile assumptions:* multi-step flows are easy to get wrong by hand.

**What we're changing and why**
- *Problem:* no single, discoverable task runner.
- *Change:* a `Makefile` with `make up`, `make seed`, `make test`, `make bench` wrapping the
  compose + seed + test + benchmark flows.
- *Out of scope:* cross-platform task runners (Taskfile/just) — Make is ubiquitous enough.
- *Design decisions / rejected alternatives:* **Make over shell aliases** — self-documenting,
  standard. Targets map 1:1 to the phases above.
- *Migrations:* depends on the compose/seed/bench targets existing (Phases 0/3).

### 4.3 — Demo GIF (optional deployed FE) `[ ]`

**What exists today (snapshot)**
- *Behavior:* no demo media; the UI must be run locally to be seen.
- *Contracts:* n/a.
- *Edge cases / fragile assumptions:* `app.js` hardcodes `allowedPorts=[8080]` and `localhost`,
  so any deployed frontend needs a configurable backend base URL first.

**What we're changing and why**
- *Problem:* reviewers shouldn't have to run the stack to see it works.
- *Change:* record a short GIF (login → transfer → history → CSV download) embedded in the README;
  optionally deploy the static frontend pointed at a configurable backend URL.
- *Out of scope:* hosting the full sharded backend publicly (cost/complexity).
- *Design decisions / rejected alternatives:* **GIF over a hosted live demo** — zero infra,
  always works. Deploying the FE is a nice-to-have gated on making the backend URL configurable.
- *Migrations:* a deployed FE needs the backend base URL externalized in `app.js` (small config
  change) and the token flow (1.3).

### 4.4 — Update CLAUDE.md `[ ]`

**What exists today (snapshot)**
- *Behavior:* `CLAUDE.md` accurately documents the **current** hand-started, three-tier,
  Compass-imported architecture and its footguns.
- *Contracts:* it's the guidance future contributors (and Claude) rely on.
- *Edge cases / fragile assumptions:* once Phases 0–3 land, several "things that will bite you"
  (manual data load, manual indices, `server.exe`, hardcoded URI) will be **obsolete** and the
  doc would mislead.

**What we're changing and why**
- *Problem:* stale architecture docs are worse than none.
- *Change:* update `CLAUDE.md` to the compose-based architecture, new layout
  (`internal/config|service|store|auth|httpx`), env config, JWT auth, and the new
  test/benchmark commands; move now-historical notes to a "legacy/manual path" appendix.
- *Out of scope:* none — purely documentation, done last.
- *Design decisions / rejected alternatives:* update at the **end** so it reflects the final
  shape rather than a moving target.
- *Migrations:* none; reflects migrations done in earlier phases.

---

## Cross-cutting issues surfaced during the snapshot (folded into changes above)

These are the concrete defects this plan addresses; listed here as a checklist so none are lost:

- [x] Transaction writes run on `context.Background()`, **not** the session context → the
  multi-document transaction is a no-op; transfers aren't atomic. *(1.2)*
- [x] Check-then-act balance race (TOCTOU) allows concurrent overdraft. *(1.2)*
- [x] Self-withdrawal skips the funds check (`… && senderID != receiverID`) → negative balances. *(1.2)*
- [x] Post-commit balance re-read uses a **secondary** → `updated_balance` can be stale. *(1.2)*
- [x] `GetClient()` nil-check is unsynchronized → possible double-dial. *(0.2)*
- [ ] No HTTP server timeouts or graceful shutdown. *(3.1)*
- [x] Unchecked type assertions / ignored `Atoi` errors in `login.go`. *(1.3)*
- [x] No authorization: any client can act as any `user_id`/`sender_id`. *(1.3)*
- [x] CORS `*` and method-guard boilerplate copy-pasted into all four handlers. *(1.4)*
- [x] "Failed transaction" rows written for non-transaction errors (lookup/parse). *(1.4)*
- [x] Seed generator emits plaintext passwords while login expects bcrypt (uncommitted hashing
  step) → seeding not reproducible. *(0.5)*
- [ ] Load harness mutates a shared `counter` across goroutines (data race); average-only stats;
  `rand.Seed` inside the loop. *(3.3)*
- [x] Committed, platform-specific `server.exe`. *(0.1)*
- [x] Money stored as ambiguous whole-dollar ints, formatted as `$%.2f`. *(1.2, Open Decision)*

---

## Interview narrative this plan is built to support
> "I built a horizontally-scalable banking backend on a sharded, replicated MongoDB cluster.
> I load-tested it and measured throughput scaling near-linearly from 1 to 6 stateless app
> instances. Along the way I found that the money-transfer path *looked* transactional but its
> writes were issued outside the session context, so it wasn't actually atomic — and it had a
> check-then-act balance race on top. I fixed both by moving the writes inside the session and
> replacing the read-then-update with a conditional `$inc` guarded by `current_balance >= amount`,
> aborting when nothing matched. I also tuned the read path: history reads serve from secondaries
> with local read concern for throughput, while balance reads use the primary with majority concern
> — trading strict consistency for read throughput exactly where it was safe to."
