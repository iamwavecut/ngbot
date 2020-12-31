FROM golang:alpine as builder
ENV CGO_ENABLED=1 GO111MODULE=on GOOS=linux GOARCH=amd64
WORKDIR /build
RUN apk add --update git bash gcc musl-dev tzdata ca-certificates build-base && rm -rf /var/cache/apk/*

COPY package/go.mod package/go.sum ./
RUN go mod download 

COPY package ./package
RUN cd package && \
    go build -ldflags='-w -s -extldflags "-static"' -o /build/ngbot ./cmd/ngbot && \
    chmod +x /build/ngbot

FROM gcr.io/distroless/static
WORKDIR /app
COPY --from=builder /build/ngbot .
COPY dist ./dist
CMD ["./ngbot"]
EXPOSE 9123
