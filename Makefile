COMPOSE ?= docker compose
SERVICE ?= cli
ENV_FILE ?= .env
DEST ?=
STORAGE ?=

.PHONY: help build test upload shell clean

help:
	@printf '%s\n' \
		'Objetivos disponibles:' \
		'  make build                     Construye la imagen del contenedor' \
		'  make test                      Ejecuta go test ./...' \
		'  make upload DEST=...           Sube ./source al destino remoto indicado' \
		'  make upload DEST=... STORAGE=...  Fuerza storage concreto para esta ejecucion' \
		'  make shell                     Abre una shell dentro del contenedor runtime' \
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

clean:
	$(COMPOSE) down --rmi local --remove-orphans
