# Pentaract CLI

Go CLI to upload and download files to/from Pentaract, always running inside a container.

## Recommended workflow

1. Create `.env` from [.env.example](./.env.example).
2. Build the image:

```bash
make build
```

### Upload

Place the files to upload in `./source`, then run:

```bash
make upload DEST=backups/2026
```

To force a specific storage for a single run:

```bash
make upload DEST=backups/2026 STORAGE="My Storage"
```

### Download

Download files from a remote path to `./downloaded_files`:

```bash
make download SRC=backups/2026
```

To force a specific storage:

```bash
make download SRC=backups/2026 STORAGE="My Storage"
```

## Makefile

The project is managed primarily via [Makefile](./Makefile):

- `make help`: show available targets.
- `make build`: build the container image.
- `make test`: run `go test ./...`.
- `make upload DEST=...`: upload `./source` to the given remote path.
- `make download SRC=...`: download remote path to `./downloaded_files`.
- `make shell`: open a shell inside the runtime container.
- `make clean`: remove the local image created by Compose.

## Configuration

Supported variables in `.env`:

- `PENTARACT_BASE_URL`: Pentaract base URL. Can be `http://host:8080` or `http://host:8080/api`.
- `PENTARACT_EMAIL`: Pentaract user.
- `PENTARACT_PASSWORD`: user password.
- `PENTARACT_STORAGE`: default storage name or ID.
- `PENTARACT_SOURCE_DIR`: defaults to `/source`.
- `PENTARACT_RETRIES`: retries per file.
- `PENTARACT_RETRY_DELAY`: delay between retries.

## Behavior

### Upload
- Walks `./source`, bind-mounts it to `/source`, and preserves the relative directory structure in the remote destination.
- Uploads are streamed, so the entire file is never loaded into memory.
- Shows per-file progress and global progress.
- Automatically retries each file on failure.
- Only uploads regular files. Symlinks, devices, and special entries are skipped.
- Empty directories are not created remotely.

### Download
- Recursively lists all files under the specified remote path.
- Downloads each file to `./downloaded_files`, preserving the relative directory structure.
- Streams directly to disk without buffering the full file in memory.
- Automatically retries each file on failure with exponential backoff.
