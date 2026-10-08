SHELL := /bin/bash
.PHONY: load all build e2e test-py load-webhooks test test-race test-short invariants generate rebuild lint clean db-up db-down db-migrate db-reset help

# ============================================================================
# Configuration
# ============================================================================

GO       := C:/Users/Nicholas\ Ho/sdk/go/bin/go.exe
GOFLAGS  :=
MODULE   := github.com/nicholasgasior/crossdock
BIN_DIR  := bin
BIN      := $(BIN_DIR)/crossdock

# Test database — defaults to the dev compose DB.
# Override with: make test CROSSDOCK_TEST_DB_URL=postgres://...
CROSSDOCK_TEST_DB_URL     ?= postgres://crossdock:crossdock@127.0.0.1:5433/crossdock?sslmode=disable
CROSSDOCK_DATABASE_URL    ?= postgres://crossdock:crossdock@127.0.0.1:5433/crossdock?sslmode=disable
CROSSDOCK_WAREHOUSE_ID    ?= wh-demo

export CROSSDOCK_TEST_DB_URL
export CROSSDOCK_DATABASE_URL
export CROSSDOCK_WAREHOUSE_ID

# ============================================================================
# Default target
# ============================================================================

all: lint build test  ## Run lint, build, and tests

# ============================================================================
# Build
# ============================================================================

build: ## Build the crossdock binary
	@mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN) ./cmd/crossdock/...

# ============================================================================
# Testing
# ============================================================================

test: ## Run all tests (requires CROSSDOCK_TEST_DB_URL)
	$(GO) test $(GOFLAGS) ./... -timeout 300s

test-race: ## Run tests with the race detector
	$(GO) test $(GOFLAGS) -race ./... -timeout 300s -count=1

test-py: ## Slotting job unit tests (needs cmd/slotting/.venv)
	cd cmd/slotting && (.venv/Scripts/python.exe -m unittest test_slotting || .venv/bin/python -m unittest test_slotting)

test-short: ## Run non-integration tests only
	$(GO) test $(GOFLAGS) -short ./... -timeout 30s

invariants: ## Run the invariant tests (1-7) against a real DB
	$(GO) test $(GOFLAGS) -race -run 'TestInvariant|TestAllocationReservationBound|TestCancelReleasesExactlyOnce|TestTrace|TestCountUnderLoad|TestRealLossIsNotHidden|TestEveryBreakTypeIsGeneratedAndCaught|TestRejectedParcelLoopShowsInOneTimeline' ./internal/... -v -timeout 300s

# End to end: needs the API (CROSSDOCK_API_URL) and the web app running with the
# credentials in web/.env.local. Each run gets a fresh generated warehouse so
# earlier runs cannot leave tasks or orders behind that change the result.
e2e: build ## Run the Playwright scanner cycle against a running stack
	@WH=wh-e2e-$$(date +%s); 	  CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) $(BIN) generate "E2E $$WH" $$WH && SL=; if [ -x cmd/slotting/.venv/Scripts/python.exe ] || [ -x cmd/slotting/.venv/bin/python ]; then CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) $(BIN) generate history -warehouse $$WH && CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) $(BIN) slotting run -warehouse $$WH && SL=1; fi && 	  cd web && set -a && . ./.env.local && set +a && 	  E2E_WAREHOUSE=$$WH E2E_SLOTTING=$$SL E2E_OPERATOR=$${E2E_OPERATOR:-olga} npx playwright test

load: ## Allocation burst, pick storm, offline sync (needs the stack up and CROSSDOCK_* env; see loadtest/load_test.go)
	$(GO) test -tags load -count=1 -timeout 30m -v ./loadtest

load-webhooks: build ## Webhook fan-out load scenario (needs Redpanda: docker compose ... up -d redpanda)
	CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) CROSSDOCK_BROKERS=$${CROSSDOCK_BROKERS:-127.0.0.1:19092} 	  $(GO) run ./cmd/webhook-load -events 100 -out loadtest/results/webhook-fanout-latest.json

# ============================================================================
# Code generation and data
# ============================================================================

generate: build db-migrate ## Generate synthetic demo data
	CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) \
	$(BIN) generate "Demo Warehouse"

rebuild: build ## Rebuild the positions projection from the movements log
	CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) \
	CROSSDOCK_WAREHOUSE_ID=$(CROSSDOCK_WAREHOUSE_ID) \
	$(BIN) rebuild positions

# ============================================================================
# Database management
# ============================================================================

db-up: ## Start the development database (Docker Compose)
	docker compose -f deploy/docker-compose.dev.yml up -d
	@echo "Waiting for database to be ready..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		docker compose -f deploy/docker-compose.dev.yml exec db pg_isready -U crossdock -d crossdock && break || sleep 2; \
	done

db-down: ## Stop the development database
	docker compose -f deploy/docker-compose.dev.yml down

db-migrate: build ## Apply all pending migrations
	CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) \
	$(BIN) migrate up

db-migrate-down: build ## Roll back one migration
	CROSSDOCK_DATABASE_URL=$(CROSSDOCK_DATABASE_URL) \
	$(BIN) migrate down

db-reset: db-down db-up db-migrate generate ## Drop, restart, and reseed the database

# ============================================================================
# Lint
# ============================================================================

lint: ## Run go vet (golangci-lint if available)
	$(GO) vet ./...
	@if command -v golangci-lint &>/dev/null; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping (go vet passed)"; \
	fi

# ============================================================================
# Cleanup
# ============================================================================

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

# ============================================================================
# Help
# ============================================================================

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'
