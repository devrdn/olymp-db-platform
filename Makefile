# DB Contest — development tasks.
#
# Run `make help` for the list.

BACKEND     := backend
COMPOSE     := docker compose -f deploy/docker-compose.yml
COMPOSE_DEV := $(COMPOSE) -f deploy/docker-compose.dev.yml
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# Local DSNs used by `make run` and `make migrate-up` against the dev overlay.
CORE_DB_DSN ?= postgres://dbcontest:dbcontest@localhost:5432/dbcontest_core?sslmode=disable
REDIS_ADDR  ?= localhost:6379

.DEFAULT_GOAL := help
.PHONY: help build test test-race cover lint vet fmt tidy run migrate-up migrate-down migrate-version dev-up dev-down dev-logs compose-up compose-down check

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## --- Go ---------------------------------------------------------------------

build: ## Compile both binaries into backend/bin
	cd $(BACKEND) && go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/api ./cmd/api
	cd $(BACKEND) && go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/migrate ./cmd/migrate

test: ## Run the unit tests
	cd $(BACKEND) && go test ./...

test-race: ## Run the tests with the race detector
	cd $(BACKEND) && go test -race ./...

cover: ## Run the tests and open the coverage report
	cd $(BACKEND) && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

vet: ## Run go vet
	cd $(BACKEND) && go vet ./...

fmt: ## Format the Go sources
	cd $(BACKEND) && gofmt -w -s .

tidy: ## Sync go.mod and go.sum
	cd $(BACKEND) && go mod tidy

check: fmt vet test ## Format, vet and test — run before pushing

run: ## Run the API against the dev infrastructure
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" REDIS_ADDR="$(REDIS_ADDR)" \
		ENV=development LOG_LEVEL=debug go run ./cmd/api

## --- Migrations -------------------------------------------------------------

migrate-up: ## Apply pending migrations
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" go run ./cmd/migrate up

migrate-down: ## Roll back the most recent migration
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" go run ./cmd/migrate down

migrate-version: ## Print the current schema version
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" go run ./cmd/migrate version

## --- Containers -------------------------------------------------------------

dev-up: ## Start PostgreSQL and Redis for local development
	$(COMPOSE_DEV) up -d pg-core redis

dev-down: ## Stop the development infrastructure
	$(COMPOSE_DEV) down

dev-logs: ## Follow the development infrastructure logs
	$(COMPOSE_DEV) logs -f

compose-up: ## Build and start the full stack
	VERSION=$(VERSION) $(COMPOSE) up -d --build

compose-down: ## Stop the full stack
	$(COMPOSE) down
