SHELL := /bin/bash
.DEFAULT_GOAL := help
export GOFLAGS := -buildvcs=false

-include .env
export

.PHONY: help
help: ## Show targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n",$$1,$$2}'

.PHONY: setup
setup: ## Install dashboard dependencies and create .env if missing
	@test -f .env || (cp .env.example .env && echo "created .env — edit it before continuing")
	pnpm install

.PHONY: gen
gen: ## Regenerate code from SQL and OpenAPI contracts
	go generate ./apps/api/...
	pnpm -C apps/dashboard gen:api

.PHONY: db-up
db-up: ## Start Postgres only
	docker compose up -d --wait postgres

.PHONY: api
api: db-up ## Run the API on the host (uses .env)
	go run ./apps/api/cmd/wise-api serve

.PHONY: runtime
runtime: ## Run the agent runtime on the host (needs the API on :8080)
	cd apps/agent-runtime && WISE_API_URL=http://127.0.0.1:8080 WISE_REPO_ROOTS="$$WISE_REPO_ROOTS" uv run python -m wise_runtime

.PHONY: dashboard
dashboard: ## Run the dashboard dev server (proxies /api to :8080)
	pnpm -C apps/dashboard dev

.PHONY: dev
dev: db-up ## Run API + dashboard on the host
	@trap 'kill 0' EXIT; go run ./apps/api/cmd/wise-api serve & \
	  (sleep 5 && $(MAKE) --no-print-directory runtime) & pnpm -C apps/dashboard dev

.PHONY: migrate
migrate: ## Apply migrations (make migrate ARGS=status|down|reset)
	go run ./apps/api/cmd/wise-api migrate $(ARGS)

.PHONY: up
up: ## Build and start the full stack in Docker
	docker compose up -d --build --wait

.PHONY: down
down: ## Stop the stack (data volume is kept)
	docker compose down

.PHONY: logs
logs: ## Follow stack logs
	docker compose logs -f

.PHONY: test
test: test-go test-runtime test-dashboard ## Run all tests

.PHONY: test-go
test-go: db-up ## Go unit + integration tests (integration tests use the compose Postgres)
	WISE_TEST_DATABASE_URL="$${WISE_TEST_DATABASE_URL:-$${WISE_DATABASE_URL:-postgres://wise:$${WISE_POSTGRES_PASSWORD}@127.0.0.1:$${WISE_POSTGRES_PORT:-5433}/wise?sslmode=disable}}" go test -count=1 ./...

.PHONY: test-runtime
test-runtime: ## Python agent runtime tests
	cd apps/agent-runtime && uv run pytest -q

.PHONY: test-dashboard
test-dashboard:
	pnpm -C apps/dashboard typecheck
	pnpm -C apps/dashboard test

.PHONY: lint
lint: ## Static checks
	go vet ./...
	test -z "$$(gofmt -l apps migrations packages)"
	cd apps/agent-runtime && uv run ruff check . && uv run ruff format --check .
	pnpm -C apps/dashboard typecheck

.PHONY: build
build: ## Build the API binary and dashboard bundle
	go build -o bin/wise-api ./apps/api/cmd/wise-api
	pnpm -C apps/dashboard build
