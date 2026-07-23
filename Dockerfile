FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.0.1-dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /rss-social ./cmd/rss-social

FROM alpine:3 AS certs
RUN apk add --no-cache ca-certificates

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /rss-social /rss-social

USER 65532:65532
VOLUME /data
EXPOSE 11080

ENV RSS_SOCIAL_DATA_DIR=/data \
    RSS_SOCIAL_LISTEN=:11080 \
    RSS_SOCIAL_ADMIN_LISTEN=127.0.0.1:11090 \
    RSS_SOCIAL_LOG_FORMAT=json

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/rss-social", "healthcheck"]

ENTRYPOINT ["/rss-social"]
CMD ["serve"]
