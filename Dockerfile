FROM golang:alpine AS build
HEALTHCHECK NONE

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 \
    go build -ldflags='-w -s -extldflags "-static"' -o ngbot cmd/ngbot/main.go && chmod +x ngbot

FROM gcr.io/distroless/static-debian12
HEALTHCHECK NONE
WORKDIR /app
ENV HOME=/root
COPY --from=build /build/ngbot ./
ENTRYPOINT ["./ngbot"]
