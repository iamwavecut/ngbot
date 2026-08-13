# syntax=docker/dockerfile:1.7
FROM golang:1.25.12-alpine AS build
HEALTHCHECK NONE

ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown

WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags="-w -s -extldflags=-static -X main.version=${VERSION} -X main.revision=${REVISION} -X main.buildDate=${BUILD_DATE}" \
      -o ngbot ./cmd/ngbot && chmod +x ngbot

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.title="ngbot" \
      org.opencontainers.image.description="Telegram gatekeeper and spam-moderation bot" \
      org.opencontainers.image.source="https://github.com/iamwavecut/ngbot" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${BUILD_DATE}"
WORKDIR /app
ENV HOME=/home/nonroot
COPY --from=build --chown=65532:65532 /build/ngbot ./
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["./ngbot", "--healthcheck=http://127.0.0.1:8080/readyz"]
ENTRYPOINT ["./ngbot"]
