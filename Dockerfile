FROM alpine:edge AS build
RUN apk update && \
    apk upgrade && \
    apk add --no-cache --update go gcc g++ bash sqlite
ENV GOPATH /go
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH=amd64 \
    #CGO_CFLAGS="-g -O2 -Wno-return-local-addr" \
    go build -ldflags='-w -s -extldflags "-static"' -o ngbot && chmod +x ngbot

FROM gcr.io/distroless/static

ARG NG_TOKEN
ARG NG_LANG=en
ARG NG_HANDLERS=admin,gatekeeper
ARG NG_LOG_LEVEL=6

ENV NG_TOKEN=${NG_TOKEN} \
    NG_LANG=${NG_LANG} \
    NG_HANDLERS=${NG_HANDLERS} \
    NG_LOG_LEVEL=${NG_LOG_LEVEL}
     
COPY --from=build /bin/sh /bin/
COPY --from=build /build/ngbot /app/
WORKDIR /app
ENTRYPOINT ["./ngbot"]
