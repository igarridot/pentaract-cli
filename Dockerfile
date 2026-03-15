FROM golang:1.23-alpine AS build

WORKDIR /app

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/pentaract-cli ./cmd/pentaract-cli

FROM alpine:3.21

WORKDIR /app

COPY --from=build /out/pentaract-cli /usr/local/bin/pentaract-cli

ENTRYPOINT ["/usr/local/bin/pentaract-cli"]
