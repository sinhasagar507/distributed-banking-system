# DISBank — A Horizontally-Scalable Banking Backend on a Sharded MongoDB Cluster

*A portfolio / networking writeup. Drop this into a Google Doc, Notion page, or LinkedIn
project, or use it as a talking-point script for recruiter calls and interviews.*

---

## 30-second elevator pitch

> I built **DISBank**, a mock banking platform whose point is to *demonstrate horizontal
> scalability*. A **stateless Go HTTP API** runs as N identical instances behind a
> **real, fully-sharded and replicated MongoDB cluster** (15 containers: 3 config servers,
> 3 query routers, and 3 shards of 3 nodes each). A custom Go load-testing harness fires
> thousands of concurrent requests and measures how latency and throughput change as you
> add server instances and shards. I then audited the system like a production service —
> found a subtle bug where money transfers *looked* transactional but weren't actually
> atomic, plus a classic check-then-act balance race — and wrote a staged plan to harden
> it (reproducible one-command stack, real authorization, concurrency-proven correctness,
> Prometheus metrics, CI, and a benchmark sweep).

The one-liner for the top of a résumé:

> **Distributed banking backend** — stateless Go API + sharded/replicated MongoDB (15-node
> cluster), containerized with Docker Compose + nginx load balancing, load-tested for
> near-linear horizontal scaling.

---

## 1. What this is and why it exists

DISBank was the final project for **CSE 512 (Distributed Systems)**. The brief wasn't "build
a bank" — it was **"demonstrate horizontal scaling."** The banking domain (users, balances,
transfers, transaction history) is just a realistic workload: it has reads, writes,
multi-document money movement, and aggregation queries, which together stress every part of a
distributed data tier.

The thesis the whole system is built to prove:

> *A stateless application tier behind a sharded, replicated database scales near-linearly —
> you add capacity by adding identical, coordination-free instances, and the database absorbs
> the load by distributing data across shards.*

Everything in the architecture exists to make that claim **measurable**.

---

## 2. Architecture

Three independent tiers, no shared state except the database — which is exactly what makes the
app tier horizontally scalable.

```
                         ┌────────────────────────────┐
   Browser / Load test ─▶│  nginx (single entrypoint)  │  round-robins the app tier
                         └──────────────┬─────────────┘
                  ┌───────────────┬─────┴──────┬────────────────┐
              ┌───▼───┐       ┌───▼───┐     ┌──▼────┐   stateless Go API
              │ app-1 │       │ app-2 │     │ app-3 │   (run N copies; no shared state)
              └───┬───┘       └───┬───┘     └───┬───┘
                  └───────────────┼─────────────┘
                       ┌──────────▼──────────┐
                       │  mongos routers ×3   │  (host ports 27151/52/53)
                       └──────────┬───────────┘
            ┌─────────────────────┼─────────────────────┐
       ┌────▼─────┐          ┌────▼─────┐          ┌─────▼────┐
       │ shard 1  │          │ shard 2  │          │ shard 3  │   each = a 3-node
       │ (rs: a/b/c)         │ (rs: a/b/c)         │ (rs: a/b/c)   replica set
       └──────────┘          └──────────┘          └──────────┘
                       ┌──────────────────────┐
                       │  config servers ×3   │  (cluster metadata, replica set)
                       └──────────────────────┘
```

### Tier 1 — Backend (Go)

- Go module `cse512`, using `gorilla/mux` for routing and the official `mongo-driver`.
- **Every process is stateless** and takes a `-p <port>` flag. Horizontal scaling = run more
  copies on more ports (8080–8085 in the load test). No locks, no leader, no session store —
  all coordination happens in MongoDB.
- Four endpoints:
  - `POST /login` — credential check (bcrypt), returns user data.
  - `POST /transaction` — move money (deposit / withdrawal / transfer).
  - `GET /transactions` — recent transaction history for a user.
  - `GET /monthdata` — a month's transactions, streamed back as a downloadable CSV.

### Tier 2 — Database (sharded MongoDB cluster)

This is the heart of the project — a **real production-style topology**, not a single `mongod`:

| Component        | Count | Role |
|------------------|-------|------|
| Config servers   | 3     | Store cluster metadata (chunk → shard mapping), as a replica set |
| `mongos` routers | 3     | Stateless query routers; clients connect here |
| Shards           | 3     | Each is a **3-node replica set** (1 primary + 2 secondaries) |
| **Total**        | **15**| containers (`mongo:4.4`) |

Key data-tier design choices:

- **Sharding strategy:** `users` is sharded on a **hashed `user_id`**; `transactions` on a
  **hashed `_id`**. Hashed shard keys give even write distribution and avoid hot-spotting on
  monotonically increasing IDs.
- **Read path tuned for throughput:** the driver reads from **secondaries**
  (`readpref.Secondary()`) with **local read concern** (`readconcern.Local()`) — trading strict
  consistency for read scalability, which is appropriate for history/reporting reads.
- **Multi-document transactions:** money movement uses MongoDB sessions
  (`StartSession → StartTransaction → CommitTransaction`). This *requires* the replica-set
  cluster — it does not work against a standalone `mongod`, which is a deliberate part of the
  "real distributed system" story.

### Tier 3 — Frontend

- Static `index.html` / `styles.css` / `app.js` served by `http-server`. Plain vanilla JS by
  design — the strength of the project is the backend/distributed-systems story, so the UI is
  kept intentionally thin.

---

## 3. The engineering deep-dives (your best interview stories)

These are the moments that show you can reason about distributed systems and concurrency, not
just wire up CRUD. Each is a real finding from auditing the codebase.

### 3.1 — "The transaction that wasn't actually a transaction"

**The setup:** `PerformTransaction` *looks* textbook-correct — it opens a session, calls
`StartTransaction()`, does the balance updates, then `CommitTransaction()`.

**The bug:** the balance `UpdateOne` calls were issued with `context.Background()` instead of
the **session context**. In `mongo-driver` v1.x, an operation only joins a transaction if it
receives the *session's* context. So in reality the two `$inc` updates ran as **independent
autocommit writes outside the transaction** — `StartTransaction` / `Commit` / `Abort` had **no
effect** on them.

**Why it's serious:** for a transfer, the sender debit could succeed and the receiver credit
fail (or the process die in between), **destroying or creating money** with no rollback. This is
the headline correctness gap for anything calling itself a "bank."

**The fix (designed):** wrap all writes in `session.WithTransaction(sc, fn)` and pass the
session context `sc` to every `UpdateOne`/`InsertOne` — which also makes the binding impossible
to forget and handles transient-error retries automatically.

> Interview gold: it teaches the difference between *invoking the transaction API* and *actually
> running inside a transaction* — a mistake that compiles, runs, and silently loses money.

### 3.2 — The check-then-act balance race (TOCTOU)

**The bug:** balance is read first (`sender.Balance < amount`), then the debit happens later.
Two concurrent transfers from the same account can both pass the sufficiency check and then both
debit — **overdrawing** the account below zero.

**The fix (designed):** replace read-then-update with a single **conditional `$inc`** executed
*inside* the transaction:

```
UpdateOne(
  { user_id: X, current_balance: { $gte: amount } },
  { $inc: { current_balance: -amount } }
)
// abort if MatchedCount == 0  → insufficient funds, atomically, no race
```

This is the canonical **lock-free** way to make balance updates safe — the database enforces the
invariant atomically instead of the application racing on a stale read.

### 3.3 — Stale balances from secondary reads

**The bug:** the post-commit balance re-read used the default **secondary** read preference, so
it could hit a replica that hadn't received the write yet — returning a **pre-transfer** balance
to the UI.

**The fix (designed):** use **primary read + majority concern** for balance-critical reads, while
keeping **secondary + local** for history/reporting reads. This is a deliberate, *per-read*
consistency-vs-throughput trade-off — exactly the kind of nuance distributed-systems interviewers
look for.

### 3.4 — Self-transactions encoded by convention

A neat domain-modeling decision: `sender_id == receiver_id` encodes a **self-transaction**
(positive amount = deposit, negative = withdrawal); `sender_id != receiver_id` is a transfer.
The same convention is baked into both the API and the mock-data generator so the two stay
consistent. (The audit also caught that the self-*withdrawal* path skipped the funds check —
another overdraw vector, fixed by applying the same `$gte` guard.)

---

## 4. Load / performance harness (how the scaling claim is measured)

`responsetime/main.go` is a custom Go benchmarking harness:

- Spins up a pool of **concurrent worker goroutines** (e.g. 100 workers, 1000 requests).
- Spreads traffic across the six backend instances (8080–8085).
- Three scenarios — `testLogin`, `testTransaction`, `testMonthlyTransactions` — exercising
  read-heavy, write-heavy, and aggregation paths respectively.
- Reports total time, average latency, requests/sec, and failure counts.

**The experiment:** sweep the number of app instances (1 → 6) and shards (1 → 3) and watch
throughput climb and latency hold — the empirical proof of horizontal scaling.

The audit also flagged the harness's *own* weaknesses (a shared `counter` mutated across
goroutines without synchronization = a data race that skews results; average-only stats hide tail
latency), with a planned rework to **p50/p95/p99 percentiles + CSV output** for reproducible
charts. Knowing that *averages hide the tail that actually matters in a scaling story* is itself a
strong signal.

---

## 5. Production-hardening: from course project to portfolio piece

A distinguishing part of this work is the **`IMPROVEMENT_PLAN.md`** — a staged plan that treats
the course project like a system going to production. Each change is documented *before any code*
with a "what exists today" snapshot (behavior, data flow, contracts, fragile assumptions) and a
"what we're changing and why" (problem statement, scope boundaries, rejected alternatives,
migrations). This is the artifact that reads as **production-minded engineering**.

**Phase 0 — Reproducibility (largely done):**
- Removed a committed platform-specific binary; cleaned up repo hygiene.
- **Externalized all config** to environment variables (`internal/config`) with safe defaults —
  the same binary now runs on a laptop *and* in a container without a recompile.
- Fixed an **unsynchronized lazy-init race** in the Mongo client using `sync.Once`.
- **Multi-stage Dockerfile** (distroless/static, non-root) for a tiny, CVE-light image.
- **One-command full stack** via `docker-compose.yml`: it declares all 15 Mongo containers with
  **healthchecks**, a one-shot **init container** that scripts replica-set initiation, shard
  wiring, collection sharding, and **index creation** (replacing manual MongoDB Compass steps),
  then 3 app instances behind an **nginx** load balancer — replacing the old client-side
  "pick a random port" hack with a real, explainable LB tier.

**Phases 1–4 — designed and specified (the roadmap):**
- **Correctness & security:** the atomicity + race + stale-read fixes above; a service/store layer
  for testability; real **JWT authorization** (today there's authentication but *no
  authorization* — any client can act as any user); shared HTTP middleware; honest error logging.
- **Testing & CI:** unit tests for money math, a **concurrency test that asserts conservation of
  money** under simultaneous transfers (proving the fix, not asserting it), and GitHub Actions CI
  that spins up the compose stack.
- **Observability & benchmarks:** `/health` + `/ready`, structured `slog` logging, graceful
  shutdown, **Prometheus metrics with latency histograms** (+ optional Grafana), and a committed
  benchmark sweep with charts.
- **Docs & demo:** README with a Mermaid architecture diagram, trade-offs section, and a Makefile.

> The honest framing for a recruiter: *the original distributed architecture is real and works;
> the improvement plan is where I demonstrate how I'd take it from "course project" to
> "production service" — including a security/correctness audit that found a money-losing
> atomicity bug.*

---

## 6. Tools & techniques

| Area | What I used |
|------|-------------|
| **Language / API** | Go, `gorilla/mux`, official MongoDB `mongo-driver` |
| **Database** | MongoDB 4.4 — **sharded cluster**, **replica sets**, **multi-document transactions**, hashed shard keys, tuned read preference / read concern, secondary reads |
| **Concurrency** | Goroutines, `sync.WaitGroup`, `sync/atomic`, `sync.Once`; lock-free conditional updates (`$inc` + `$gte`); TOCTOU analysis |
| **Infra / DevOps** | Docker, Docker Compose (15-container topology with healthchecks + init container), multi-stage builds, distroless images, **nginx load balancing**, 12-factor env config |
| **Performance** | Custom Go concurrent load-testing harness; latency/throughput measurement; scaling sweeps; (planned) p50/p95/p99 + CSV |
| **Security** | bcrypt password hashing; (planned) JWT auth + authorization middleware |
| **Data** | Faker-based mock-data generation (Node) — 200K users / 1.5M transactions; manual index design |
| **Engineering practice** | Pre-change design docs, snapshot-then-change discipline, rejected-alternatives reasoning, staged migration planning, code audit |

---

## 7. Skills this project demonstrates

- **Distributed systems fundamentals** — sharding, replication, routing, consistency models, the
  CAP-style trade-offs you make per-read (throughput vs. staleness).
- **Horizontal scalability by design** — stateless services, externalized coordination, load
  balancing, and *measuring* the scaling claim rather than asserting it.
- **Concurrency correctness** — recognizing and fixing a non-atomic "transaction," a TOCTOU race,
  and stale-read hazards; choosing lock-free database-enforced invariants.
- **Production infrastructure** — containerization, one-command reproducible environments,
  healthcheck-gated startup ordering, load balancers.
- **Engineering judgment** — reading existing code critically, documenting trade-offs, scoping
  changes, and planning migrations that keep the system working at every step.

---

## 8. Ready-to-use talking points

**If asked "tell me about a project":**
> "I built a horizontally-scalable banking backend on a sharded, replicated MongoDB cluster and
> load-tested it to show throughput scaling as I add stateless app instances. The most
> interesting part was the audit: the money-transfer path *looked* transactional, but the writes
> were issued outside the session context, so it wasn't actually atomic — and it had a
> check-then-act balance race on top. I'd fix both by moving the writes inside the session and
> replacing the read-then-update with a conditional `$inc` guarded by `current_balance >= amount`,
> aborting when nothing matched. I also separated the read path: history reads serve from
> secondaries for throughput, while balance reads use the primary with majority concern — trading
> consistency for throughput exactly where it's safe."

**If asked "what was hard / what did you learn":**
> "That invoking a transaction API isn't the same as running inside a transaction. The code
> compiled, ran, and silently moved money non-atomically. It taught me to verify *behavior* —
> e.g. with a conservation-of-money test under concurrency — not just trust that the API call
> implies the guarantee."

**If asked "what would you do next":**
> "Real authorization with JWTs (right now there's authentication but no authorization), a
> concurrency test that proves money conservation, Prometheus metrics for tail latency, CI that
> brings up the whole compose stack, and a committed benchmark sweep with charts."

---

## 9. Honest status (so you never get caught out)

- **Working today:** the full sharded/replicated cluster, the stateless Go API and its four
  endpoints, the load-testing harness, mock-data generation, and the Phase-0 reproducibility work
  (env config, Dockerfile, one-command Docker Compose stack with nginx + init container).
- **Designed and specified, not yet implemented:** the correctness/security fixes (atomicity,
  TOCTOU, JWT authorization), the unit/concurrency tests, CI, and the observability/metrics work.
  These are fully documented in `IMPROVEMENT_PLAN.md` — and the *audit that found the bugs* is
  itself a strong, defensible talking point.

Present it as: **a real distributed system that works, plus a rigorous plan (and audit) for
taking it to production.** That combination — working system *and* the judgment to see what's
missing — is exactly what hiring managers want to hear.
```
