FROM golang:alpine AS build
HEALTHCHECK NONE
RUN apk update && apk upgrade && apk add --no-cache --update \
    gcc g++ sqlite
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
ARG NG_TOKEN
ARG NG_LANG=en
ARG NG_HANDLERS=admin,gatekeeper
ARG NG_LOG_LEVEL=6

ENV NG_TOKEN=${NG_TOKEN} \
    NG_LANG=${NG_LANG} \
    NG_HANDLERS=${NG_HANDLERS} \
    NG_LOG_LEVEL=${NG_LOG_LEVEL}

COPY --from=build /build/ngbot /app/
WORKDIR /app
USER 1001
ENTRYPOINT ["./ngbot"]
