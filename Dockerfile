FROM golang:alpine as builder
WORKDIR /build
RUN apk add --update git bash gcc musl-dev tzdata ca-certificates build-base && rm -rf /var/cache/apk/*

COPY go.mod go.sum ./
RUN go mod download 

COPY . .
RUN GO111MODULE=on GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o ./ngbot ./cmd/ngbot && chmod +x ./ngbot

FROM gcr.io/distroless/base
COPY --from=builder /build/ngbot .
COPY --from=builder /build/resources ./resources
COPY --from=builder /build/etc ./etc

EXPOSE 9123
