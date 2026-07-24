FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.0.1
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /rss-expert ./cmd/rss-expert

FROM alpine:3 AS prep
RUN apk add --no-cache ca-certificates \
    && mkdir -p /prepared/data \
    && chown -R 65532:65532 /prepared/data

FROM scratch
COPY --from=prep /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=prep --chown=65532:65532 /prepared/data /data
COPY --from=build /rss-expert /rss-expert

USER 65532:65532
VOLUME /data
EXPOSE 11080

ENV RSS_EXPERT_DATA_DIR=/data \
    RSS_EXPERT_LISTEN=:11080 \
    RSS_EXPERT_LOG_FORMAT=json

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/rss-expert", "healthcheck"]

ENTRYPOINT ["/rss-expert"]
CMD ["serve"]
