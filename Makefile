# DB Contest — development tasks.
#
# Run `make help` for the list.

BACKEND     := backend
ENV_FILE    := deploy/.env
COMPOSE     := docker compose -f deploy/docker-compose.yml
COMPOSE_DEV := $(COMPOSE) -f deploy/docker-compose.dev.yml
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# The host-side targets talk to the same containers as Compose does, so they
# read the same credentials. Hard-coding them here would mean two sources of
# truth that silently disagree the moment somebody changes a password.
-include $(ENV_FILE)

CORE_DB_USER     ?= dbcontest
CORE_DB_NAME     ?= dbcontest_core
CORE_DB_PORT     ?= 5432
REDIS_PORT       ?= 6379
ADMIN_LOGIN      ?= admin
ADMIN_NAME       ?= System Administrator

CORE_DB_DSN ?= postgres://$(CORE_DB_USER):$(CORE_DB_PASSWORD)@localhost:$(CORE_DB_PORT)/$(CORE_DB_NAME)?sslmode=disable

# Redis is optional: with no address the service uses its in-process cache.
# Set REDIS_ADDR in deploy/.env to use the container instead.
ifdef REDIS_PASSWORD
REDIS_ADDR ?= redis://:$(REDIS_PASSWORD)@localhost:$(REDIS_PORT)/0
endif
REDIS_ADDR ?=

# Security tools are installed on demand into the Go bin directory, so a fresh
# checkout can run the full gate without a separate setup step.
GOBIN  := $(shell go env GOPATH)/bin
GOVULN := $(GOBIN)/govulncheck
GOSEC  := $(GOBIN)/gosec

.DEFAULT_GOAL := help
.PHONY: help require-env build test test-race cover lint vet fmt tidy run migrate-up migrate-down migrate-version bootstrap compose-bootstrap compose-observability dev-up dev-down dev-logs compose-up compose-down check fmt-check tidy-check vuln sec test-all

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## --- Go ---------------------------------------------------------------------

build: ## Compile the binaries into backend/bin
	cd $(BACKEND) && go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/api ./cmd/api
	cd $(BACKEND) && go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/migrate ./cmd/migrate
	cd $(BACKEND) && go build -trimpath -o bin/bootstrap ./cmd/bootstrap

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

## --- Quality gate -----------------------------------------------------------

fmt-check: ## Fail if the sources are not gofmt-clean (does not modify them)
	@cd $(BACKEND) && out=$$(gofmt -l -s .); \
		test -z "$$out" || { echo "not gofmt-clean:"; echo "$$out"; exit 1; }

tidy-check: ## Fail if go.mod/go.sum are not tidy (does not modify them)
	cd $(BACKEND) && go mod tidy -diff

vuln: ## Scan dependencies and the toolchain for known vulnerabilities
	@test -x $(GOVULN) || go install golang.org/x/vuln/cmd/govulncheck@latest
	cd $(BACKEND) && $(GOVULN) ./...

sec: ## Static security analysis
	@test -x $(GOSEC) || go install github.com/securego/gosec/v2/cmd/gosec@latest
	cd $(BACKEND) && $(GOSEC) -exclude-generated -quiet ./...

# The full gate, mirroring the CI workflow so a green run here means a green run
# there. Unlike `check` it changes nothing on disk: every step verifies rather
# than reformats, so it is safe to run on a dirty tree.
test-all: fmt-check tidy-check vet test-race build vuln sec ## Run everything CI runs
	@echo "test-all: all checks passed."

# require-env fails with an explanation instead of letting the command reach the
# database with empty credentials and report an authentication failure.
.PHONY: require-env
require-env:
	@test -f $(ENV_FILE) || { \
		echo "$(ENV_FILE) is missing. Create it first:"; \
		echo "  cp deploy/.env.example $(ENV_FILE)"; \
		echo "then fill in the passwords."; \
		exit 1; }
	@test -n "$(CORE_DB_PASSWORD)" || { \
		echo "CORE_DB_PASSWORD is not set in $(ENV_FILE)."; exit 1; }

run: require-env ## Run the API against the dev infrastructure
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" REDIS_ADDR="$(REDIS_ADDR)" \
		ENV=development LOG_LEVEL=debug go run ./cmd/api

## --- Migrations -------------------------------------------------------------

migrate-up: require-env ## Apply pending migrations
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" go run ./cmd/migrate up

migrate-down: require-env ## Roll back the most recent migration
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" go run ./cmd/migrate down

migrate-version: require-env ## Print the current schema version
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" go run ./cmd/migrate version

bootstrap: require-env ## Create the first administrator (idempotent; prints the password)
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" go run ./cmd/bootstrap -login $(ADMIN_LOGIN) -name "$(ADMIN_NAME)"

## --- Containers -------------------------------------------------------------

dev-up: ## Start PostgreSQL and Redis for local development
	$(COMPOSE_DEV) up -d pg-core redis

dev-down: ## Stop the development infrastructure
	# dev-up starts redis by naming it, which activates its "shared" profile for
	# that command only. A plain `down` does not re-activate the profile, so it
	# would leave redis running and then refuse to remove the network it is still
	# attached to. Activating the profiles here tears down everything dev can start.
	$(COMPOSE_DEV) --profile shared --profile full down

dev-logs: ## Follow the development infrastructure logs
	$(COMPOSE_DEV) logs -f

compose-up: ## Build and start the stack (Caddy, API, database)
	VERSION=$(VERSION) $(COMPOSE) up -d --build

compose-bootstrap: require-env ## Create the first administrator inside the stack
	$(COMPOSE) --profile bootstrap run --rm bootstrap

compose-observability: ## Add Prometheus, Loki and Grafana to a running stack
	$(COMPOSE) --profile observability up -d

compose-down: ## Stop the full stack
	$(COMPOSE) --profile bootstrap --profile observability --profile shared down
