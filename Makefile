COMPOSE ?= docker compose
SERVICE ?= cli
ENV_FILE ?= .env
DEST ?=
SRC ?=
STORAGE ?=
ON_CONFLICT ?= keep_both
DOWNLOADS_PATH ?= ./downloaded_files
SCREEN_SESSION ?= pentaract-cli

.PHONY: help build test upload download shell screen clean

help:
	@printf '%s\n' \
		'Available targets:' \
		'  make build                     Build the container image' \
		'  make test                      Run go test ./...' \
		'  make upload DEST=...           Upload ./source to the given remote path' \
		'  make upload DEST=... ON_CONFLICT=skip  Skip files that already exist (same name, path, size)' \
		'  make upload DEST=... STORAGE=...  Force a specific storage for this run' \
		'  make download SRC=...          Download remote path to ./downloaded_files' \
		'  make download SRC=... STORAGE=...  Force a specific storage for this run' \
		'  make shell                     Open a shell inside the runtime container' \
		'  make screen                    Reuse or create the screen session pentaract-cli' \
		'  make clean                     Remove the local image created by compose'

build:
	$(COMPOSE) build $(SERVICE)

test:
	go test ./...

upload:
	@if [ -z "$(DEST)" ]; then echo 'Usage: make upload DEST=remote/path [STORAGE="My Storage"]'; exit 1; fi
	$(COMPOSE) run --rm $(SERVICE) upload --env-file $(ENV_FILE) $(if $(STORAGE),--storage "$(STORAGE)",) --dest "$(DEST)" --on-conflict "$(ON_CONFLICT)"

download:
	@if [ -z "$(SRC)" ]; then echo 'Usage: make download SRC=remote/path [STORAGE="My Storage"]'; exit 1; fi
	@mkdir -p $(DOWNLOADS_PATH)
	$(COMPOSE) run --rm -v $(DOWNLOADS_PATH):/downloads $(SERVICE) download --env-file $(ENV_FILE) $(if $(STORAGE),--storage "$(STORAGE)",) --src "$(SRC)"

shell:
	$(COMPOSE) run --rm --entrypoint /bin/sh $(SERVICE)

screen:
	@if screen -ls | grep -Eq '[[:digit:]]+\.$(SCREEN_SESSION)[[:space:]]'; then \
		exec screen -xRR "$(SCREEN_SESSION)"; \
	else \
		exec screen -S "$(SCREEN_SESSION)"; \
	fi

clean:
	$(COMPOSE) down --rmi local --remove-orphans
