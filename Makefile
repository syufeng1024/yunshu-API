SHELL := /bin/sh
COMPOSE := docker compose -f deploy/docker-compose.yml
GO ?= go
LOAD_ENV = set -e; set -a; if [ -f .env ]; then . ./.env; fi; set +a;

.PHONY: build test vet lint up down migrate run-gw run-ctl run-jobs check

build:
	$(GO) build ./...

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	@files="$$(gofmt -l $$(find . -name '*.go' -not -path './.git/*'))"; \
	if [ -n "$$files" ]; then echo "gofmt required:"; echo "$$files"; exit 1; fi
	$(GO) vet ./...

up:
	$(COMPOSE) up -d --wait --wait-timeout 180

down:
	$(COMPOSE) down

migrate:
	@echo "T01 has no migrations; schema migrations start in T02."

run-gw:
	@$(LOAD_ENV) $(GO) run ./cmd/gatewayd

run-ctl:
	@$(LOAD_ENV) $(GO) run ./cmd/controld

run-jobs:
	@$(LOAD_ENV) $(GO) run ./cmd/jobsd

check: vet test
	@set -e; \
	if grep -rn '"encoding/json"' --include='*.go' . | grep -v 'internal/jsonx'; then \
		echo 'FAIL: encoding/json is only allowed inside internal/jsonx'; exit 1; \
	fi; \
	hits="$$(grep -rn 'context.Background()' --include='*.go' internal/ cmd/ || true)"; \
	bad="$$(printf '%s\n' "$$hits" | grep -v '^cmd/' | grep -v '^$$' || true)"; \
	if [ -n "$$bad" ]; then echo 'FAIL: context.Background() outside cmd/'; echo "$$bad"; exit 1; fi; \
	if grep -rn 'QuantumNous' --include='*.go' --include='*.sql' .; then \
		echo 'FAIL: forbidden upstream attribution string found'; exit 1; \
	fi; \
	echo 'check: grep assertions passed'
