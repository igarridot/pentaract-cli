# CLAUDE.md

This file provides guidance to Claude Code when working with code in this repository.

## Project

Pentaract CLI is a containerized Go CLI tool that uploads and downloads files to/from a Pentaract server (a self-hosted file storage service using Telegram channels as distributed chunk storage). It runs exclusively inside Docker containers and streams transfers with automatic retries, progress tracking, and conflict resolution.

## Commands

```bash
# Build the container image
make build

# Run tests locally
make test
go test ./...

# Upload files to Pentaract
make upload DEST=backups/2026
make upload DEST=backups/2026 STORAGE="My Storage"

# Download files from Pentaract
make download SRC=backups/2026
make download SRC=backups/2026 STORAGE="My Storage"

# Open a shell in the container
make shell

# Remove local images
make clean
```

## Architecture

### Entry point: `cmd/pentaract-cli/main.go`

Signal handling (SIGINT/SIGTERM) → context cancellation → `app.Run()`.

### Key data flow

**Upload**: `runUpload()` scans source dir → collects files → pre-warms remote directory cache → `runPipelinedUploads()` with concurrency of 2 → per-file: `ResolveAvailablePath()` → `UploadFileWithProgress()` → streams multipart to server → polls SSE for progress → retries on failure.

**Download**: `runDownload()` → `walkRemoteTree()` recursively lists remote dir via `/files/tree/` → for each file: `DownloadFile()` streams GET `/files/download/` response directly to local disk → retries on failure with exponential backoff.

### Packages

- `internal/app/` — Upload orchestration (`run.go`), download orchestration (`download.go`), file discovery (`source.go`), remote path resolution (`paths.go`), CLI progress reporting (`progress.go`)
- `internal/pentaract/` — HTTP client for Pentaract API (`client.go`), data types (`types.go`)
- `internal/config/` — .env file parsing and configuration loading (`env.go`)

### Key design decisions

- **Streaming uploads**: Files are never loaded into memory. `io.MultiReader(prefix, file, suffix)` streams directly from disk to HTTP body.
- **Pipelined uploads (C1)**: Up to 2 files upload concurrently so verification of file N overlaps with upload of file N+1.
- **Exponential backoff (C2)**: `baseDelay * 2^(attempt-1) + jitter(0-25%)` instead of fixed delay.
- **Connection pooling (C3)**: Custom HTTP transport with `MaxIdleConnsPerHost=10` for concurrent upload + progress polling.
- **Directory pre-warming (C4)**: `PrewarmDirs()` fetches remote directory listings in parallel (up to 5) before uploads start.
- **Adaptive polling (C5)**: Progress poll interval varies: 500ms during verification, 2s for files >100MB, 1s default.
- **Thread-safe path planner**: `remoteNamePlanner` uses mutex for safe concurrent access from pipelined uploads.

## Configuration

All via environment variables or `.env` file (env vars override):

| Variable | Default | Purpose |
|----------|---------|---------|
| `PENTARACT_BASE_URL` | Required | Server URL (auto-appends `/api`) |
| `PENTARACT_EMAIL` | Required | Login email |
| `PENTARACT_PASSWORD` | Required | Login password |
| `PENTARACT_STORAGE` | Required | Storage ID or name |
| `PENTARACT_SOURCE_DIR` | `/source` | Local directory to upload |
| `PENTARACT_RETRIES` | `3` | Max attempts per file |
| `PENTARACT_RETRY_DELAY` | `2s` | Base delay between retries |

## Testing

**All new features and bug fixes must include tests.** Do not submit code without corresponding test coverage.

Zero external dependencies — only Go standard library.

```bash
go test ./...
```

Tests use `httptest.NewServer` for HTTP mocking. Key test files:
- `internal/pentaract/client_test.go` — Upload streaming, progress parsing, multipart envelope
- `internal/pentaract/download_test.go` — Download streaming, progress callback, error handling, partial cleanup
- `internal/app/download_test.go` — Pipelined downloads: concurrency, error propagation, cancellation
- `internal/app/paths_test.go` — Copy suffix generation
- `internal/app/source_test.go` — .gitkeep skipping
- `internal/config/env_test.go` — .env parsing

## Container enforcement

The CLI refuses to run outside a container. It checks `/.dockerenv` and `/proc/1/cgroup` for Docker/containerd/kubepods/podman markers.

## API endpoints used

| Method | Endpoint | Purpose |
|--------|----------|---------|
| `POST` | `/auth/login` | Authenticate |
| `GET` | `/storages` | List storages |
| `GET` | `/storages/{id}/files/tree/{path}` | List directory |
| `POST` | `/storages/{id}/files/upload` | Upload file (multipart) |
| `GET` | `/upload_progress?upload_id={id}` | Poll progress (SSE) |
| `GET` | `/storages/{id}/files/download/{path}` | Download file (streaming) |
