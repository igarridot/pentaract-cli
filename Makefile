COMPOSE ?= docker compose
SERVICE ?= cli
ENV_FILE ?= .env
DEST ?=
STORAGE ?=
SCREEN_SESSION ?= pentaract-cli

.PHONY: help build test upload shell screen clean

help:
	@printf '%s\n' \
		'Objetivos disponibles:' \
		'  make build                     Construye la imagen del contenedor' \
		'  make test                      Ejecuta go test ./...' \
		'  make upload DEST=...           Sube ./source al destino remoto indicado' \
		'  make upload DEST=... STORAGE=...  Fuerza storage concreto para esta ejecucion' \
		'  make shell                     Abre una shell dentro del contenedor runtime' \
		'  make screen                    Reutiliza o crea la sesion screen pentaract-cli' \
		'  make clean                     Elimina la imagen local creada por compose'

build:
	$(COMPOSE) build $(SERVICE)

test:
	go test ./...

upload:
	@if [ -z "$(DEST)" ]; then echo 'Uso: make upload DEST=ruta/remota [STORAGE="Mi Storage"]'; exit 1; fi
	$(COMPOSE) run --rm $(SERVICE) upload --env-file $(ENV_FILE) $(if $(STORAGE),--storage "$(STORAGE)",) --dest "$(DEST)"

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
