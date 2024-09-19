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
ARG NG_HANDLERS=admin,gatekeeper,reactor
ARG NG_LOG_LEVEL=6
ARG OPENAI_API_KEY
ARG OPENAI_BASE_URL=https://api.openai.com/v1
ARG OPENAI_MODEL=gpt-4o-mini

ENV NG_TOKEN=${NG_TOKEN} \
    NG_LANG=${NG_LANG} \
    NG_HANDLERS=${NG_HANDLERS} \
    NG_LOG_LEVEL=${NG_LOG_LEVEL} \
    OPENAI_API_KEY=${OPENAI_API_KEY} \
    OPENAI_BASE_URL=${OPENAI_BASE_URL} \
    OPENAI_MODEL=${OPENAI_MODEL}

COPY --from=build /build/ngbot /app/
WORKDIR /app
USER 1001
ENTRYPOINT ["./ngbot"]
