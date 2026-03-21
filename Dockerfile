FROM --platform=$BUILDPLATFORM public.ecr.aws/docker/library/golang:1.26-alpine AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/pentaract-cli ./cmd/pentaract-cli

FROM public.ecr.aws/docker/library/alpine:3.23

WORKDIR /app

COPY --from=build /out/pentaract-cli /usr/local/bin/pentaract-cli

ENTRYPOINT ["/usr/local/bin/pentaract-cli"]
