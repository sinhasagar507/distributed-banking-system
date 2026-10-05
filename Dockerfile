# syntax=docker/dockerfile:1
#
# Multi-stage build for the DISBank Go backend.
#
#   Stage 1 (builder): the full Go toolchain compiles a single static binary.
#   Stage 2 (runtime):  a near-empty base image carries ONLY that binary.
#
# The toolchain (~800 MB) never ships — the final image is just the binary on a
# minimal base, kept small and CVE-light. See IMPROVEMENT_PLAN.md §0.3.

# ---------------------------------------------------------------------------
# Stage 1 — builder
# ---------------------------------------------------------------------------
# Pinned to match go.mod (go 1.23.1). This image has the compiler, stdlib, and
# everything needed to build; none of it is carried into the final image.
FROM golang:1.23 AS builder

WORKDIR /src

# Copy only the module files first and restore dependencies in their own layer.
# Deps change rarely; source changes constantly — so this download stays cached
# and only re-runs when go.mod/go.sum actually change (not on every code edit).
COPY go.mod go.sum ./
RUN go mod download

# Now bring in the source and build.
COPY . .

# CGO_ENABLED=0 forces a fully static, pure-Go binary with zero runtime deps
# (incl. Go's pure-Go DNS resolver instead of libc getaddrinfo) — the enabling
# condition for running on a libc-less base like distroless/static.
# GOOS/GOARCH default to the build platform; set explicitly for reproducibility.
# -ldflags "-s -w" strips the symbol table and DWARF debug info to shrink the binary.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /server main.go

# ---------------------------------------------------------------------------
# Stage 2 — runtime
# ---------------------------------------------------------------------------
# distroless/static: scratch + CA certs + /etc/passwd (with a built-in nonroot
# user) + tzdata. No shell, no package manager — minimal attack surface.
# To debug interactively, swap this line for `FROM alpine:3.20` (it has a shell).
FROM gcr.io/distroless/static-debian12:nonroot

# Copy ONLY the compiled binary across from the builder. Nothing else follows.
COPY --from=builder /server /server

# Run as the unprivileged user shipped by the :nonroot image (uid 65532), so a
# compromise of the app does not start out as root.
USER nonroot:nonroot

# The app reads PORT from the environment (config.go); default it so the image
# is runnable on its own. Override with -e PORT=8081 or in docker-compose (0.4).
ENV PORT=8080

# Documents the listening port for tooling / `docker run -P`. Informational only.
EXPOSE 8080

# No shell form (distroless has no shell) — exec form runs the binary directly.
# The server picks up PORT and MONGO_URI from the environment; MONGO_URI must be
# overridden to service names (e.g. mongos1:27017) when run inside the network.
ENTRYPOINT ["/server"]
