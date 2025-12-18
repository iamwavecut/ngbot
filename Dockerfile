# syntax=docker/dockerfile:1.7
FROM golang:alpine AS build
HEALTHCHECK NONE

WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN CGO_ENABLED=0 \
    --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -ldflags='-w -s -extldflags "-static"' -o ngbot cmd/ngbot/main.go && chmod +x ngbot

FROM gcr.io/distroless/static-debian12
HEALTHCHECK NONE
WORKDIR /app
ENV HOME=/root
COPY --from=build /build/ngbot ./
ENTRYPOINT ["./ngbot"]
