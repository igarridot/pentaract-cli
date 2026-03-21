# Pentaract CLI

Go CLI to upload the contents of `./source` to Pentaract, always running inside a container.

## Recommended workflow

1. Create `.env` from [.env.example](./.env.example).
2. Place the files to upload in `./source`.
3. Build the image:

```bash
make build
```

4. Run the upload:

```bash
make upload DEST=backups/2026
```

To force a specific storage for a single run:

```bash
make upload DEST=backups/2026 STORAGE="My Storage"
```

## Makefile

The project is managed primarily via [Makefile](./Makefile):

- `make help`: show available targets.
- `make build`: build the container image.
- `make test`: run `go test ./...`.
- `make upload DEST=...`: launch the CLI inside the container.
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

- The CLI walks `./source`, bind-mounts it to `/source`, and preserves the relative directory structure in the remote destination.
- Uploads are streamed, so the entire file is never loaded into memory.
- Shows per-file progress and global progress.
- Automatically retries each file on failure.
- Only uploads regular files. Symlinks, devices, and special entries are skipped.
- Empty directories are not created remotely.
