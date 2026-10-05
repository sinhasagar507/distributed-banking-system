Please follow the below instructions to smoothly run the code in the repository.

## Environment Setup

### 1) Install Go: 
Visit URL: https://go.dev/doc/install

This Go Language tool installation is optional: https://pkg.go.dev/golang.org/x/tools/gopls#section-readme. But this will be a useful tool have when debugging and writing code.

### 2) Install Node and NPM:
Visit URL: https://nodejs.org/en/download/package-manager

### 3) Install Docker:
Install Docker by visting the URL: https://www.docker.com/get-started/

### 4) Install http-server:
Run ```npm install -g http-server``` to server http files locally using a server.

### 5) Optional Installations
- Go Language Sever and Analysis Tool: https://pkg.go.dev/golang.org/x/tools/gopls#section-readme. 
- MongoDB Compass: https://www.mongodb.com/try/download/compass


### 6) Dependency Installations:
Run ```npm install``` to install dependencies required by Javascript Code in the repository.

In case there is a problem running the Go code and it looks like a dependency issue, please run the following commands:

``` go mod vendor```

``` go mod tidy```

## Backend Configuration (environment variables)

The backend reads its deployment settings from the environment (see
`internal/config/config.go`). **Every variable is optional** — the defaults
reproduce the original local behavior, so `go run main.go -p 8080` works with no
configuration at all. A committed `.env.example` documents the full set; copy it
to `.env` and edit when you need to retarget the app:

```bash
cp .env.example .env
```

The Go binary does **not** auto-load `.env`; export the values into your shell
(`set -a; source .env; set +a`) or supply them via Docker's `environment:` block.

| Variable | Default | Purpose |
| --- | --- | --- |
| `MONGO_URI` | `mongodb://localhost:27151,localhost:27152,localhost:27153` | Comma-separated mongos routers. Inside Docker, use the router **service names** instead of `localhost`. |
| `MONGO_MAX_POOL` | `30000` | Max connection-pool size (tune for benchmarks without a recompile). |
| `MONGO_MIN_POOL` | `10` | Min connection-pool size. |
| `MONGO_READ_PREF` | `secondary` | Read preference: `primary`, `primaryPreferred`, `secondary`, `secondaryPreferred`, or `nearest`. Unknown values fall back to `secondary`. |
| `PORT` | _(unset)_ | Port to listen on. The `-p` flag overrides this; if neither is set the server refuses to start. |
| `JWT_SECRET` | `dev-insecure-secret-change-me` | Secret for signing/verifying JWTs (wired now, used in a later phase). Change for any non-local use. |
