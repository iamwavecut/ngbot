FROM golang:alpine AS build
HEALTHCHECK NONE

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    go build -ldflags='-w -s -extldflags "-static"' -o ngbot && chmod +x ngbot

FROM gcr.io/distroless/static-debian12
HEALTHCHECK NONE
USER 1000
WORKDIR /app
COPY --from=build /build/ngbot ./
ENTRYPOINT ["./ngbot"]
