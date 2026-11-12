# syntax=docker/dockerfile:1

FROM golang:1.27.4-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# Commit that was built; shown by `cirrus-nextcloud-mcp version` and by get_server_info.
# Build with: docker build --build-arg GIT_COMMIT=$(git rev-parse HEAD) .
ARG GIT_COMMIT=unknown
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${GIT_COMMIT}" \
      -o /out/cirrus-nextcloud-mcp ./cmd/cirrus-nextcloud-mcp

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
 && addgroup -S -g 10001 cirrus \
 && adduser -S -u 10001 -G cirrus -H -s /sbin/nologin cirrus
COPY --from=build /out/cirrus-nextcloud-mcp /usr/local/bin/cirrus-nextcloud-mcp
USER 10001:10001
ENV MCP_TRANSPORT=streamable-http \
    MCP_HTTP_ADDR=:8000
EXPOSE 8000
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8000/healthz >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/cirrus-nextcloud-mcp"]
