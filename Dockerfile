# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.27.2

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Download modules in their own layer so source edits do not invalidate them.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY web ./web

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/bomber-grid ./cmd/server

# Distroless static is small, includes CA certificates, and ships a nonroot user.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="Bomber Grid" \
      org.opencontainers.image.description="Server-authoritative multiplayer Bomberman clone with Go, HTMX and WebSockets"

COPY --from=build /out/bomber-grid /bomber-grid

ENV BM_ADDR=0.0.0.0:4301
ENV BM_LOG_LEVEL=info

EXPOSE 4301
USER nonroot:nonroot
STOPSIGNAL SIGTERM
ENTRYPOINT ["/bomber-grid"]
