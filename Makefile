# DB Contest — development tasks.
#
# Run `make help` for the list.

BACKEND     := backend
FRONTEND    := frontend
ENV_FILE    := deploy/.env
COMPOSE       := docker compose -f deploy/docker-compose.yml
COMPOSE_DEV   := $(COMPOSE) -f deploy/docker-compose.dev.yml
COMPOSE_BUILD := $(COMPOSE) -f deploy/docker-compose.build.yml

# Where released images live. The base compose file only names images, so a
# server pulls what CI proved instead of compiling on the machine that serves
# the olympiad.
REGISTRY         ?= ghcr.io/devrdn
IMAGE_BACKEND    := $(REGISTRY)/db-contest-backend
IMAGE_FRONTEND   := $(REGISTRY)/db-contest-frontend
# A second backend image, not a second entrypoint on the first: the Query
# Runner links PostgreSQL's parser through cgo and needs a libc at runtime, so
# it cannot share the static base the other binaries run on.
IMAGE_RUNNER     := $(REGISTRY)/db-contest-queryrunner
# What a local build is tagged with, and what `make deploy` refuses to accept
# as a deployable version. deploy/.env deliberately does not set it: an
# assignment there would be read by the -include below and would beat anything
# passed in the environment.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BACKUP_DIR  ?= deploy/backups

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
API_PORT         ?= 8080
# Where `make runner` listens and `make run` dials. One variable, because the
# Query Runner reads it as its listen address and the API as the address to
# reach it at, and a development stack where those two disagree is a stack
# where the console is silently off.
QUERY_RUNNER_ADDR ?= localhost:9100

CORE_DB_DSN ?= postgres://$(CORE_DB_USER):$(CORE_DB_PASSWORD)@localhost:$(CORE_DB_PORT)/$(CORE_DB_NAME)?sslmode=disable
# The game cluster, as the provisioning role. Only the development overlay
# publishes this port; the stack itself keeps the cluster off the host.
GAME_DB_DSN ?= postgres://$(GAME_DB_USER):$(GAME_DB_PASSWORD)@localhost:$(GAME_DB_PORT)/$(GAME_DB_NAME)?sslmode=disable

# The database the DB-backed tests run against: on the same server as the
# product's, and never the product's own. The tests used to be handed
# CORE_DB_DSN itself, and every run left a few hundred fixture accounts in the
# database `make run` serves from. A test database is one whose name ends in
# _test; the tests refuse to connect to anything else, and cmd/testdb refuses
# to recreate anything else (internal/platform/storage/storagetest).
CORE_TEST_DB_NAME ?= $(CORE_DB_NAME)_test
CORE_TEST_DB_DSN  ?= postgres://$(CORE_DB_USER):$(CORE_DB_PASSWORD)@localhost:$(CORE_DB_PORT)/$(CORE_TEST_DB_NAME)?sslmode=disable

# Redis is optional: with no address the service uses its in-process cache.
#
# deploy/.env also sets REDIS_ADDR — for the containerized api service, which
# reaches Redis by the compose-internal hostname `redis`. A process on the
# host cannot resolve that name, so that value must never reach `make run`.
# `?=` will not shadow it: once -include reads the line, Make considers the
# variable "defined" even when its value is empty, and `?=` only fires on a
# variable that is entirely undefined. This assignment is therefore
# unconditional, computed straight from REDIS_PASSWORD instead. A value given
# on the command line (`make REDIS_ADDR=... run`) still wins — Make always
# prefers a command-line assignment over one written in a makefile, no matter
# where in the file it appears.
REDIS_ADDR := $(if $(REDIS_PASSWORD),redis://:$(REDIS_PASSWORD)@localhost:$(REDIS_PORT)/0,)

# Where an uploaded dump lands when the API runs on the host.
#
# deploy/.env sets GAME_UPLOAD_DIR to the container's own path, because that is
# the mount point compose gives the api service. A process on the host cannot
# write there — /var/lib is root's — and gamefile.NewStore does not fall back:
# it MkdirAlls the directory and app.go refuses to start when that fails. So
# forwarding the compose value would turn "uploads are off" into "the API does
# not come up", which is worse.
#
# Assigned unconditionally for the same reason REDIS_ADDR above is: once
# -include has read the line, `?=` will not fire. A command-line assignment
# still wins.
GAME_UPLOAD_DIR := $(CURDIR)/deploy/game-uploads

# Where the *browser* is when the interface runs on the host, which is not the
# same question as where the API is. Behind Caddy the two are one origin and
# this is unnecessary; `make run` has no Caddy, so Next's rewrite forwards
# /api/* to :8080 and replaces Host on the way, and the one request a browser
# sends this API directly — a chunk of an uploaded dump — arrives looking
# cross-origin. Everything else that writes goes through a server action and
# carries no Origin at all, which is why nothing else notices.
FRONT_ORIGIN ?= http://localhost:$(FRONT_PORT)
FRONT_PORT   ?= 3000

# Where the interface reaches the API when both run on the host. Inside compose
# the address is the service name; from a process on the host it is loopback,
# which is the same translation CORE_DB_DSN above makes for the database.
FRONT_API_ORIGIN := http://localhost:$(API_PORT)

# Security tools are installed on demand into the Go bin directory, so a fresh
# checkout can run the full gate without a separate setup step.
GOBIN  := $(shell go env GOPATH)/bin
GOVULN := $(GOBIN)/govulncheck
GOSEC  := $(GOBIN)/gosec

.DEFAULT_GOAL := help
.PHONY: help require-env require-version build test test-race test-db test-game game-orphans api-contract audit-contract proto proto-check static-check backup restore restore-check images images-push deploy deploy-api deploy-web deployed cover lint vet fmt tidy run migrate-up migrate-down migrate-version bootstrap stack-bootstrap stack-observability dev-up dev-observability dev-db-ui dev-down dev-logs stack-up stack-down check fmt-check tidy-check vuln sec test-all front front-install front-check front-build front-start front-test front-lint

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

## --- Go ---------------------------------------------------------------------

build: ## Compile the binaries into backend/bin
	cd $(BACKEND) && go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/api ./cmd/api
	cd $(BACKEND) && go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/migrate ./cmd/migrate
	cd $(BACKEND) && go build -trimpath -o bin/bootstrap ./cmd/bootstrap
	cd $(BACKEND) && go build -trimpath -ldflags="-X main.version=$(VERSION)" -o bin/queryrunner ./cmd/queryrunner
	cd $(BACKEND) && go build -trimpath -o bin/gamedb ./cmd/gamedb
	cd $(BACKEND) && go build -trimpath -o bin/gameorphans ./cmd/gameorphans

test: ## Run the unit tests
	cd $(BACKEND) && go test ./...

test-race: ## Run the tests with the race detector
	cd $(BACKEND) && go test -race ./...

# The repository tests run their SQL against a real PostgreSQL, most inside a
# transaction that is rolled back so they leave nothing behind; a few — where
# the point is what happens between two separate connections, not one, such
# as queryproxy's own guarantee that it and postgres.Registrations agree
# about what "started" means — commit their own fixture and delete it
# afterwards instead. Without CORE_DB_DSN they skip rather than failing: a
# developer with no database to hand can still run `make test`. This target
# is what makes sure the SQL is actually exercised — `make dev-up` first.
#
# Against CORE_TEST_DB_DSN, recreated and migrated from zero by test-db-reset
# before every run: nothing a previous run left behind survives into this
# one, and nothing this run does reaches the product's database.
# internal/platform/storage is in the list for storagetest's own proof that
# it refuses a database that is not a test database.
test-db: require-env test-db-reset ## Run the repository tests against a fresh test database
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_TEST_DB_DSN)" go test -count=1 ./internal/platform/storage/... ./internal/postgres/... ./internal/provisioning/... ./internal/queryproxy/...

# Once per run and before any test binary starts: `go test` runs packages in
# parallel, and they share this database. The migrations are applied by the
# same command a deployment uses.
.PHONY: test-db-reset
test-db-reset: require-env
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_TEST_DB_DSN)" go run ./cmd/testdb
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_TEST_DB_DSN)" go run ./cmd/migrate up

# The game cluster tests connect as the participant's own database role and
# provoke what it must not be able to do. They cannot be faked: every guarantee
# under test is a refusal by PostgreSQL, not by our code. `make dev-up` first —
# it starts pg-game along with the core database.
#
# The three role passwords travel with the DSN because these tests prepare the
# cluster, and preparing it states what game_reader, game_writer and
# game_author authenticate with. They are the same roles `make runner` connects as, so a
# harness with passwords of its own would take a running Query Runner's
# credentials away mid-session; given the deployment's own, a test run writes
# back what is already there and both keep working. Missing, the tests stop
# and say so rather than inventing a password (internal/gamedb/gamedbtest).
GAME_ROLE_PASSWORDS := GAME_READER_PASSWORD="$(GAME_READER_PASSWORD)" GAME_WRITER_PASSWORD="$(GAME_WRITER_PASSWORD)" GAME_AUTHOR_PASSWORD="$(GAME_AUTHOR_PASSWORD)"

test-game: require-env ## Run the game cluster tests against the development cluster
	cd $(BACKEND) && GAME_DB_DSN="$(GAME_DB_DSN)" $(GAME_ROLE_PASSWORDS) \
		go test -count=1 ./internal/gamedb/... ./internal/queryrunner/... ./internal/rpc/...

# The one test that crosses both clusters: a script saved in the core database
# has to become a real database on the game cluster. Every other test of that
# feature stops at a boundary, which is how BuildTemplate went months with no
# caller at all.
.PHONY: test-game-build
test-game-build: require-env test-db-reset
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_TEST_DB_DSN)" GAME_DB_DSN="$(GAME_DB_DSN)" $(GAME_ROLE_PASSWORDS) \
		go test -count=1 -run TestAScriptSavedInTheCoreDatabase ./internal/provisioning/

# The contract between the Core API and the Query Runner. Generated code is
# committed, so a checkout builds without protoc and CI needs no toolchain for
# it. `make proto` regenerates; `make proto-check` is what actually notices a
# .proto edited without regenerating, and it is deliberately not part of
# `make check`, which must keep working for a contributor with no protoc.
proto: ## Regenerate the Query Runner contract from proto/
	cd $(BACKEND) && protoc --proto_path=proto \
		--go_out=. --go_opt=module=github.com/devrdn/db-contest/backend \
		--go-grpc_out=. --go-grpc_opt=module=github.com/devrdn/db-contest/backend \
		proto/queryrunner/v1/queryrunner.proto

# Fails when the committed Go does not match the .proto — the drift that
# otherwise shows up as a contract change nobody's code has.
proto-check: ## Fail if the generated contract is out of date
	@command -v protoc >/dev/null 2>&1 || { echo "protoc not installed; skipping the contract check"; exit 0; }
	@$(MAKE) --no-print-directory proto
	@git diff --quiet -- $(BACKEND)/internal/rpc/queryrunnerv1 || { \
		echo "the generated contract is out of date; run 'make proto' and commit the result"; \
		git --no-pager diff --stat -- $(BACKEND)/internal/rpc/queryrunnerv1; \
		exit 1; }
	@echo "the generated contract matches proto/"

cover: ## Run the tests and open the coverage report
	cd $(BACKEND) && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

vet: ## Run go vet
	cd $(BACKEND) && go vet ./...

fmt: ## Format the Go sources
	cd $(BACKEND) && gofmt -w -s .

tidy: ## Sync go.mod and go.sum
	cd $(BACKEND) && go mod tidy

# The API's error vocabulary, published as data for the interface to check its
# messages against. A test fails when the committed file is out of date, so
# this is never something anybody has to remember on their own.
api-contract: ## Regenerate docs/api/error-codes.json from the declared codes
	cd $(BACKEND) && go run ./cmd/apicontract

# The audit trail's action vocabulary, published the same way and for the same
# reason: dictionary.test.ts checks the interface's translations against this
# file rather than a hand-typed copy of it, and cmd/auditcontract's own test
# fails `go test ./...` when this file falls behind audit.Actions().
audit-contract: ## Regenerate docs/api/audit-actions.json from audit.Actions()
	cd $(BACKEND) && go run ./cmd/auditcontract

# The Core API and the one-shot jobs must build without cgo, because they run
# on a base image with no libc at all. It is an easy thing to lose: one import
# of the SQL checker anywhere in their dependency graph pulls PostgreSQL's
# parser in, and nothing else notices until the image build fails — or worse,
# until an image is published that cannot start.
static-check: ## Fail if the API's binaries have picked up a cgo dependency
	@cd $(BACKEND) && CGO_ENABLED=0 go build -o /dev/null ./cmd/api ./cmd/migrate ./cmd/bootstrap ./cmd/gameorphans \
		&& echo "api, migrate, bootstrap and gameorphans build without cgo"

check: fmt vet static-check test ## Format, vet and test — run before pushing

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

# GAME_PROVISIONER_DSN is what turns the whole game circuit on: without it the
# composition root skips provisioning entirely, so a contest's game cannot be
# written or built and /contests/{id}/game is never even mounted — a 404 that
# no amount of restarting fixes. In development the provisioning role is the
# same superuser the game-cluster tests use; a deployment keeps it apart from
# the participant roles (see config.GameProvisionerDSN).
#
# QUERY_RUNNER_ADDR points at `make runner`. Without it the console endpoint
# is never mounted, so a participant can neither run a query nor see the
# schema panel — the panel hangs off the same service, because a schema is
# only worth showing on a screen where queries can be run.
#
# Loopback is trusted so the interface running on the host (`make front`) may
# hand a forwarded address on, the way the web container does behind Caddy in
# production. In plain dev there is no proxy and no chain, so requests still
# log as ::1 — the line exists so the dev API treats a forwarded header the
# same way production does the moment something does send one.
run: require-env ## Run the API against the dev infrastructure
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" REDIS_ADDR="$(REDIS_ADDR)" \
		GAME_PROVISIONER_DSN="$(GAME_DB_DSN)" \
		GAME_AUTHOR_PASSWORD="$(GAME_AUTHOR_PASSWORD)" \
		GAME_UPLOAD_DIR="$(GAME_UPLOAD_DIR)" \
		PUBLIC_ORIGINS="$(FRONT_ORIGIN)" \
		QUERY_RUNNER_ADDR="$(QUERY_RUNNER_ADDR)" \
		TRUSTED_PROXIES="127.0.0.1,::1" \
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


## --- Interface --------------------------------------------------------------
#
# The frontend runs on the host during development, like the API does, and takes
# its one setting from the same deploy/.env: API_ORIGIN is derived here rather
# than duplicated in a second env file, so there is one place to change a port.
#
# Switching between development and production locally is a choice of target:
# `front` runs the development server, `front-build` + `front-start` run the
# production one — which is the only way to see the production behaviour that
# differs, the content policy among it.

front: front-install ## Run the interface against the dev API
	cd $(FRONTEND) && API_ORIGIN="$(FRONT_API_ORIGIN)" npm run dev

front-install: ## Install the interface's dependencies if they are missing
	@test -d $(FRONTEND)/node_modules || (cd $(FRONTEND) && npm ci)

front-build: front-install ## Build the interface for production
	cd $(FRONTEND) && npm run build

# COOKIE_SECURE=false is not optional here, and it is the whole reason this
# line has a comment. `next start` sets NODE_ENV=production, but this serves
# over plain http on localhost with no proxy and no certificate — and a browser
# silently discards a Secure cookie delivered over http. Without this, signing
# in appears to succeed and the very next click goes back to the form.
front-start: front-build ## Serve the production build against the dev API
	cd $(FRONTEND) && API_ORIGIN="$(FRONT_API_ORIGIN)" COOKIE_SECURE=false npm run start

front-test: front-install ## Run the interface's tests
	cd $(FRONTEND) && npm test

front-lint: front-install ## Lint the interface
	cd $(FRONTEND) && npm run lint

front-check: front-install ## Everything CI runs for the interface
	cd $(FRONTEND) && npm run lint && npm run contrast && npm run type-scale \
		&& npm run error-codes && npm run typecheck && npm test && npm run build

## --- Containers -------------------------------------------------------------

## --- Backups ----------------------------------------------------------------
#
# On-premise means nobody else is backing this machine up. What is in the core
# database — the participants' answers and the results of an olympiad — cannot
# be reconstructed by reinstalling anything, so this is the one piece of
# operations that is not optional.
#
# The dump runs inside the container, so no PostgreSQL client is needed on the
# host, and it goes through the same credentials as everything else in this
# file.

backup: require-env ## Dump the core database into deploy/backups/
	@mkdir -p $(BACKUP_DIR)
	@file=$(BACKUP_DIR)/$(CORE_DB_NAME)-$$(date +%Y%m%d-%H%M%S).dump; \
		$(COMPOSE) exec -T pg-core \
			pg_dump --format=custom --no-owner --username=$(CORE_DB_USER) $(CORE_DB_NAME) > $$file || \
			{ echo "backup failed; removing the partial file"; rm -f $$file; exit 1; }; \
		test -s $$file || { echo "the dump is empty — refusing to keep it"; rm -f $$file; exit 1; }; \
		echo "wrote $$file ($$(du -h $$file | cut -f1))"; \
		echo "Copy it off this machine. A backup that only exists on the host it came from is not a backup."

# Proves the dump is loadable without touching anything real: it is restored
# into a throwaway database that is dropped again immediately. Run it after
# every change to the schema — the difference between having backups and
# believing you do is exactly this command.
restore-check: require-env ## Verify a dump can be loaded (FILE=path)
	@test -n "$(FILE)" || { echo "usage: make restore-check FILE=$(BACKUP_DIR)/....dump"; exit 1; }
	@test -f "$(FILE)" || { echo "no such file: $(FILE)"; exit 1; }
	@scratch=restore_check_$$$$; \
		$(COMPOSE) exec -T pg-core createdb --username=$(CORE_DB_USER) $$scratch; \
		if $(COMPOSE) exec -T pg-core \
			pg_restore --username=$(CORE_DB_USER) --dbname=$$scratch --no-owner --exit-on-error < "$(FILE)"; then \
			tables=$$($(COMPOSE) exec -T pg-core psql --username=$(CORE_DB_USER) --dbname=$$scratch -tAc \
				"SELECT count(*) FROM information_schema.tables WHERE table_schema='public'"); \
			echo "$(FILE) restores cleanly: $$tables tables"; result=0; \
		else \
			echo "$(FILE) DOES NOT restore — this backup cannot be relied on"; result=1; \
		fi; \
		$(COMPOSE) exec -T pg-core dropdb --username=$(CORE_DB_USER) $$scratch; \
		exit $$result

# Replaces the live database. Everything currently in it is gone.
#
# The API is stopped for the duration when it is running: pg_restore cannot
# drop objects a live service holds open, and a half-restored database serving
# requests is worse than a stopped one. It is started again afterwards whether
# the restore worked or not — and if that fails, it says so, because a silent
# "restored" over a service that never came back is the worst of both.
restore: require-env ## Replace the core database from a dump (FILE=path CONFIRM=yes)
	@test -n "$(FILE)" || { echo "usage: make restore FILE=$(BACKUP_DIR)/....dump CONFIRM=yes"; exit 1; }
	@test -f "$(FILE)" || { echo "no such file: $(FILE)"; exit 1; }
	@test "$(CONFIRM)" = "yes" || { \
		echo "This REPLACES the contents of $(CORE_DB_NAME) with $(FILE)."; \
		echo "Everything currently in it — participants, answers, results — is lost."; \
		echo "Re-run with CONFIRM=yes when that is what you mean."; exit 1; }
	@serving=$$($(COMPOSE) ps --status=running --services 2>/dev/null | grep -cx api || true); \
		if [ "$$serving" = "1" ]; then echo "stopping api for the restore"; $(COMPOSE) stop api; fi; \
		$(COMPOSE) exec -T pg-core \
			pg_restore --username=$(CORE_DB_USER) --dbname=$(CORE_DB_NAME) \
				--clean --if-exists --no-owner --exit-on-error < "$(FILE)"; \
		result=$$?; \
		if [ "$$serving" = "1" ]; then \
			$(COMPOSE) start api || echo "the api did NOT come back up — start it by hand"; fi; \
		if [ $$result -eq 0 ]; then echo "restored $(CORE_DB_NAME) from $(FILE)"; \
		else echo "restore FAILED — the database is in an unknown state, do not run an olympiad on it"; fi; \
		exit $$result

# The two halves of the game circuit that are not the API.
#
# `game-roles` is the same one-shot job the deploy runs (cmd/gamedb): it
# creates the participant roles the Query Runner connects as, the game_author
# role an organiser's game script runs as, and the restrictions all three work
# under. Idempotent, so running it again is how a
# restriction added in a later release reaches a cluster that already exists.
#
# `runner` is the Query Runner itself. A separate process on purpose — it
# links PostgreSQL's parser through cgo to check a query before running it,
# which is C code reading text an adversary chose, and a crash there must not
# be a way to end sign-in and the timer (cmd/queryrunner's own doc). It is
# also the only process that holds the participant roles' credentials, which
# is why the API above is given the provisioner's and never these.
.PHONY: game-roles
game-roles: require-env ## Create the game cluster's participant roles
	cd $(BACKEND) && GAME_DB_ADMIN_DSN="$(GAME_DB_DSN)" \
		GAME_READER_PASSWORD="$(GAME_READER_PASSWORD)" \
		GAME_WRITER_PASSWORD="$(GAME_WRITER_PASSWORD)" \
		GAME_AUTHOR_PASSWORD="$(GAME_AUTHOR_PASSWORD)" \
		go run ./cmd/gamedb

# The repair for a database the core database has already written off while
# the database itself is still on the cluster. Nothing in the product ever
# reclaims one — the sweep skips a row that already says 'dropped' — so this
# is the only thing that can. It prints what it would remove and stops;
# `make ARGS=-apply game-orphans` is what actually drops them.
.PHONY: game-orphans
game-orphans: require-env ## List (ARGS=-apply to remove) databases the core database calls dropped that are still on the cluster
	cd $(BACKEND) && CORE_DB_DSN="$(CORE_DB_DSN)" GAME_DB_ADMIN_DSN="$(GAME_DB_DSN)" \
		go run ./cmd/gameorphans $(ARGS)

.PHONY: runner
runner: require-env ## Run the Query Runner against the dev game cluster
	cd $(BACKEND) && \
		GAME_DB_DSN="postgres://game_reader:$(GAME_READER_PASSWORD)@localhost:$(GAME_DB_PORT)/postgres?sslmode=disable" \
		GAME_DB_WRITER_DSN="postgres://game_writer:$(GAME_WRITER_PASSWORD)@localhost:$(GAME_DB_PORT)/postgres?sslmode=disable" \
		ENV=development LOG_LEVEL=debug go run ./cmd/queryrunner

dev-up: ## Start PostgreSQL and Redis for local development
	$(COMPOSE_DEV) up -d pg-core pg-game redis

# Prometheus, Loki, Promtail and Grafana, without pulling in the containerized
# api/caddy/pg-core (same reasoning as stack-observability below). Combine
# it with dev-up in one command the way `make` already supports running
# several targets: `make dev-up dev-observability`.
#
# Metrics: Prometheus is already configured with a second scrape job
# (core-api-dev, see deploy/observability/prometheus.yml) pointed at
# host.docker.internal:9090, which is how `make run`'s API — bound to all
# interfaces on the host — becomes visible without any extra flag.
#
# Logs: this does NOT give you `make run`'s logs in Grafana. Promtail
# discovers what to scrape through the Docker socket, so it can only see
# containers; a bare `go run` process on the host has no container to find.
# Watch its own terminal (or your own `tee` to a file) for dev logs — the
# Loki/Grafana log view only ever shows the containerized api.
dev-observability: ## Add Prometheus, Loki, Promtail and Grafana to the dev stack
	$(COMPOSE_DEV) --profile observability up -d prometheus loki promtail grafana

# A browser UI for the core database (Adminer) — dev only. Its login screen
# already knows the server (pg-core); type in CORE_DB_USER / CORE_DB_PASSWORD
# / CORE_DB_NAME from deploy/.env to connect.
dev-db-ui: ## Add a database browser (Adminer) to the dev stack
	$(COMPOSE_DEV) --profile dbui up -d adminer

dev-down: ## Stop the development infrastructure
	# dev-up starts redis by naming it, which activates its "shared" profile for
	# that command only. A plain `down` does not re-activate the profile, so it
	# would leave redis running and then refuse to remove the network it is still
	# attached to. Activating the profiles here tears down everything dev can start.
	$(COMPOSE_DEV) --profile shared --profile observability --profile dbui --profile full down

dev-logs: ## Follow the development infrastructure logs
	$(COMPOSE_DEV) logs -f

images: ## Build the release images from this working tree
	VERSION=$(VERSION) $(COMPOSE_BUILD) build api queryrunner web
	@echo "built $(IMAGE_BACKEND):$(VERSION), $(IMAGE_RUNNER):$(VERSION) and $(IMAGE_FRONTEND):$(VERSION)"

# CI publishes these from a tag, and only from a tag: a merge proves the image
# can be built, a tag says somebody meant to ship it. This target exists for
# the day the registry is unreachable from CI and somebody has to do it by
# hand.
images-push: images ## Push the release images to the registry
	docker push $(IMAGE_BACKEND):$(VERSION)
	docker push $(IMAGE_RUNNER):$(VERSION)
	docker push $(IMAGE_FRONTEND):$(VERSION)

## --- Deployment -------------------------------------------------------------
#
# Pull-based and deliberate: the server fetches a named version, nothing
# reaches in from outside. That is what keeps a production SSH key out of a
# cloud CI system and keeps a merge from restarting a service in the middle of
# a running olympiad.
#
# VERSION is required rather than defaulted. `make deploy` picking up whatever
# the working tree describes as is exactly how the wrong thing gets deployed.

require-version:
	@test -n "$(filter-out dev,$(VERSION))" || { 		echo "name the version to deploy, for example:"; 		echo "  make deploy VERSION=v1.4.0"; 		echo "Rolling back is the same command with the previous version."; 		exit 1; }

deploy: require-env require-version ## Deploy a version of everything (VERSION=vX.Y.Z)
	VERSION=$(VERSION) $(COMPOSE) pull
	VERSION=$(VERSION) $(COMPOSE) up -d
	@echo "deployed $(VERSION)"

# One service at a time, which is how a frontend release leaves the API — and
# every session it is serving — untouched.
deploy-api: require-env require-version ## Deploy only the API (VERSION=vX.Y.Z)
	VERSION=$(VERSION) $(COMPOSE) pull api migrate
	VERSION=$(VERSION) $(COMPOSE) up -d api
	@echo "deployed api $(VERSION)"

deploy-web: require-env require-version ## Deploy only the interface (VERSION=vX.Y.Z)
	VERSION=$(VERSION) $(COMPOSE) pull web
	VERSION=$(VERSION) $(COMPOSE) up -d web
	@echo "deployed web $(VERSION)"

deployed: ## Print the versions currently running
	@$(COMPOSE) ps --format '{{.Service}}\t{{.Image}}' 2>/dev/null || \
		echo "nothing is running from this compose project"

## --- The whole stack --------------------------------------------------------
#
# Three families, and the difference between them is worth keeping straight:
#
#   dev-*     the infrastructure only, with the API and the interface run from
#             your editor against it. The everyday loop.
#   stack-*   everything, in containers. `stack-up` BUILDS from this working
#             tree — uncommitted changes included — and tags the images with
#             the commit, or <commit>-dirty. Nothing is pulled.
#   deploy*   a named release, pulled from the registry. Nothing is built.
#
# `stack-up` is not "production": it is production's shape, from your code.
# A server runs `make deploy VERSION=...`, which cannot build anything at all.

stack-up: ## Build the whole stack from this working tree and start it
	VERSION=$(VERSION) $(COMPOSE_BUILD) up -d --build

stack-bootstrap: require-env ## Create the first administrator inside the stack
	$(COMPOSE) --profile bootstrap run --rm bootstrap

stack-observability: ## Add Prometheus, Loki, Promtail and Grafana without touching the rest
	# Services with no `profiles:` key (api, caddy, pg-core, migrate) are
	# Compose's "default" set and start on ANY `up`, profile flag or not —
	# `--profile` only lifts the gate on profiled services, it never narrows
	# the run. Naming the services explicitly is what actually limits `up`
	# to them; --profile is still required or these four would be skipped
	# as "not in an active profile".
	$(COMPOSE) --profile observability up -d prometheus loki promtail grafana

stack-down: ## Stop the full stack
	$(COMPOSE) --profile bootstrap --profile observability --profile shared down
