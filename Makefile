SHELL := /bin/sh
COMPOSE := docker compose -f deploy/docker-compose.yml
STACK := docker compose --env-file deploy/.env.stack -f deploy/docker-compose.stack.yml
GO ?= go
LOAD_ENV = set -e; set -a; if [ -f .env ]; then . ./.env; fi; set +a;

.PHONY: build test vet lint up down migrate run-gw run-ctl run-jobs check \
        stack-env stack-up stack-build stack-down stack-ps stack-logs stack-smoke

build:
	$(GO) build ./...

test:
	$(GO) test -p 2 ./...

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
	@$(LOAD_ENV) $(GO) run ./cmd/migrate up

run-gw:
	@$(LOAD_ENV) $(GO) run ./cmd/gatewayd

run-ctl:
	@$(LOAD_ENV) $(GO) run ./cmd/controld

run-jobs:
	@$(LOAD_ENV) $(GO) run ./cmd/jobsd

check: vet test

# 全栈容器化部署：MySQL + Redis + 三进程 + 前端，一条命令起完整站点。
# 与上面的 up/down（只跑依赖）互不影响，project name 不同。
stack-env:
	@if [ -f deploy/.env.stack ]; then echo "deploy/.env.stack 已存在，跳过"; \
	else cp deploy/.env.stack.example deploy/.env.stack; \
	  sed -i.bak "s|^SESSION_SECRET=$$|SESSION_SECRET=$$(openssl rand -base64 32)|" deploy/.env.stack; \
	  sed -i.bak "s|^CHANNEL_KEY_KEK=$$|CHANNEL_KEY_KEK=$$(openssl rand -base64 32)|" deploy/.env.stack; \
	  sed -i.bak "s|^GATEWAY_METRICS_TOKEN=$$|GATEWAY_METRICS_TOKEN=$$(openssl rand -hex 16)|" deploy/.env.stack; \
	  rm -f deploy/.env.stack.bak; \
	  echo "已生成 deploy/.env.stack（含随机密钥），请复核 MYSQL_ROOT_PASSWORD"; fi

stack-build: stack-env
	$(STACK) build

stack-up: stack-env
	$(STACK) up -d --build --wait --wait-timeout 300
	@echo "站点 http://localhost:$$(grep -E '^WEB_PORT=' deploy/.env.stack | cut -d= -f2)"

stack-down:
	$(STACK) down

stack-ps:
	$(STACK) ps

stack-logs:
	$(STACK) logs -f --tail=100

# 冒烟：迁移是否落库、三进程是否 ready、站点是否能吐出首页。
stack-smoke:
	@echo "--- 迁移版本 ---"
	@docker exec yunshu-stack-mysql mysql -uroot -p"$$(grep -E '^MYSQL_ROOT_PASSWORD=' deploy/.env.stack | cut -d= -f2)" -N \
	  -e "SELECT version FROM yunshu.schema_migrations ORDER BY version DESC LIMIT 5" 2>/dev/null || echo "（迁移表查询失败）"
	@echo "--- 表数量 ---"
	@docker exec yunshu-stack-mysql mysql -uroot -p"$$(grep -E '^MYSQL_ROOT_PASSWORD=' deploy/.env.stack | cut -d= -f2)" -N \
	  -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='yunshu'" 2>/dev/null
	@echo "--- 就绪探针 ---"
	@curl -s -o /dev/null -w "gatewayd /readyz %{http_code}\n" http://127.0.0.1:18080/readyz
	@curl -s -o /dev/null -w "controld /readyz %{http_code}\n" http://127.0.0.1:18081/readyz
	@curl -s -o /dev/null -w "web      /          %{http_code}\n" http://127.0.0.1:8088/
	@curl -s -o /dev/null -w "web      /healthz   %{http_code}\n" http://127.0.0.1:8088/healthz

ci: check
	@bash scripts/lint.sh
	@set -e; \
	if grep -rn '"encoding/json"' --include='*.go' . | grep -v 'internal/jsonx'; then \
		echo 'FAIL: encoding/json is only allowed inside internal/jsonx'; exit 1; \
	fi; \
	hits="$$(grep -rn 'context.Background()' --include='*.go' internal/ cmd/ || true)"; \
	bad="$$(printf '%s\n' "$$hits" | grep -v '^cmd/' | grep -v '^$$' || true)"; \
	if [ -n "$$bad" ]; then echo 'FAIL: context.Background() outside cmd/'; echo "$$bad"; exit 1; fi; \
	if grep -rn 'QuantumNous' --include='*.go' --include='*.sql' .; then \
		echo 'FAIL: new-api source string in Go/SQL — do not copy AGPL code (errata E-23)'; exit 1; \
	fi; \
	echo 'check: grep assertions passed'
