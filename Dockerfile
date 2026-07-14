# syntax=docker/dockerfile:1.7
FROM golang:1.25.12-alpine AS build
HEALTHCHECK NONE

WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -ldflags='-w -s -extldflags "-static"' -o ngbot cmd/ngbot/main.go && chmod +x ngbot

FROM gcr.io/distroless/static-debian12:nonroot
HEALTHCHECK NONE
WORKDIR /app
ENV HOME=/home/nonroot
COPY --from=build --chown=65532:65532 /build/ngbot ./
USER nonroot:nonroot
ENTRYPOINT ["./ngbot"]
