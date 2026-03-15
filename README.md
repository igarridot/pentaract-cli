# Pentaract CLI

CLI en Go para subir a Pentaract el contenido de `./source` usando siempre un contenedor.

## Flujo recomendado

1. Crea `.env` a partir de [.env.example](./.env.example).
2. Deja los archivos a subir en `./source`.
3. Construye la imagen:

```bash
make build
```

4. Ejecuta la subida:

```bash
make upload DEST=backups/2026
```

Si quieres forzar el storage en una ejecución concreta:

```bash
make upload DEST=backups/2026 STORAGE="Mi Storage"
```

## Makefile

El proyecto se maneja principalmente con [Makefile](/Volumes/SUNEAST/workspace/pentaract-cli/Makefile):

- `make help`: muestra las operaciones disponibles.
- `make build`: construye la imagen Docker.
- `make test`: ejecuta `go test ./...`.
- `make upload DEST=...`: lanza la CLI dentro del contenedor.
- `make shell`: abre una shell en el contenedor runtime.
- `make clean`: elimina la imagen local creada por Compose.

## Configuracion

Variables soportadas en `.env`:

- `PENTARACT_BASE_URL`: URL base de Pentaract. Puede ser `http://host:8080` o `http://host:8080/api`.
- `PENTARACT_EMAIL`: usuario de Pentaract.
- `PENTARACT_PASSWORD`: password del usuario.
- `PENTARACT_STORAGE`: nombre o ID del storage por defecto.
- `PENTARACT_SOURCE_DIR`: por defecto `/source`.
- `PENTARACT_RETRIES`: reintentos por fichero.
- `PENTARACT_RETRY_DELAY`: espera entre reintentos.

## Comportamiento

- La CLI recorre `./source`, lo monta como bind volume en `/source` y preserva la estructura relativa en el destino remoto.
- La subida es por streaming, así que no carga el fichero completo en memoria.
- Muestra progreso por fichero y progreso global.
- Reintenta automáticamente cada fichero.
- Los reintentos suben primero a una ruta temporal y solo mueven al destino final cuando Pentaract confirma la subida.
- Solo sube ficheros regulares. Symlinks, dispositivos y entradas especiales se omiten.
- Los directorios vacíos no se crean en remoto.
