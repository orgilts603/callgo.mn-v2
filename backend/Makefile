# CallGo.mn backend
GO      ?= go
BIN     := bin/server
PKGS    := ./...
IMAGE   ?= callgo-backend

.PHONY: help build run test test-race lint fmt migrate-status docker clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

build: ## Build the server binary into bin/server
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN) ./cmd/server

run: ## Run the server (loads .env if present)
	$(GO) run ./cmd/server

test: ## Run all tests
	$(GO) test $(PKGS)

test-race: ## Run all tests with the race detector
	$(GO) test -race -count=1 $(PKGS)

lint: ## Static analysis (go vet)
	$(GO) vet $(PKGS)

fmt: ## Format all Go code
	gofmt -s -w .

migrate-status: ## Placeholder: migrations run automatically on server start
	@echo "Migrations are embedded (internal/crm/migrations) and applied on server start."
	@echo "Inspect state with: psql \"$${CALLGO_DATABASE_URL:-postgres://callgo:callgo@localhost:5432/callgo?sslmode=disable}\" -c 'select * from schema_migrations'"

docker: ## Build the container image
	docker build -t $(IMAGE) .

clean: ## Remove build output
	rm -rf bin
