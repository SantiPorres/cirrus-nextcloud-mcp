COMMIT  ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
COMPOSE := docker compose -f test/integration/docker-compose.test.yml

.PHONY: build test lint fmt vuln check integration integration-up integration-down

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/cirrus-nextcloud-mcp ./cmd/cirrus-nextcloud-mcp

test:
	go test -race ./...

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

check: lint test

# Integration tests run against a real Nextcloud started with Docker.
integration-up:
	$(COMPOSE) up -d --wait

integration-down:
	$(COMPOSE) down -v

integration: integration-up
	go test -tags integration -count=1 ./test/integration/... ; status=$$? ; $(COMPOSE) down -v ; exit $$status
