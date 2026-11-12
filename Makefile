# TrailTransfer — developer & demo tasks.
# Plain Go + Docker Compose; no extra build framework required.

BINARY      := trailtransfer
PKG         := ./cmd/trailtransfer
JOURNAL     := ./evidence/journal.jsonl
VERSION     ?= dev

GO          ?= go
COMPOSE     ?= docker compose

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## ---- Build & quality ------------------------------------------------------

.PHONY: build
build: ## Build the worker binary
	$(GO) build -ldflags "-X github.com/RainerGewalt/trailtransfer/internal/version.Version=$(VERSION)" -o $(BINARY) $(PKG)

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -w ./cmd ./internal

.PHONY: test
test: ## Run unit tests with the race detector
	$(GO) test -race ./...

.PHONY: cover
cover: ## Run tests and print per-package coverage
	$(GO) test -cover ./...

.PHONY: lint
lint: ## gofmt check + go vet (fails on unformatted files)
	@unformatted="$$(gofmt -l ./cmd ./internal)"; \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...

.PHONY: vuln
vuln: ## Scan dependencies for known vulnerabilities
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

## ---- Demo & verification --------------------------------------------------

.PHONY: demo
demo: ## Start the Mosquitto + S3 (rclone serve s3) + worker demo (docker compose up --build)
	$(COMPOSE) up --build

.PHONY: demo-down
demo-down: ## Stop and remove demo containers
	$(COMPOSE) down -v

.PHONY: negative-paths
negative-paths: ## Negative-path scenarios without Docker (docs/TESTING.md, L4); needs mosquitto and rclone; NEG_PORT (default 15931)
	./scripts/negative-paths.sh

.PHONY: first-use
first-use: ## First-use check (docs/TESTING.md, L5): README and Quickstart from a fresh clone; RELEASE=x.y.z checks the published release
	RELEASE=$(RELEASE) ./scripts/first-use.sh

.PHONY: verify-demo
verify-demo: build ## Independently verify the evidence journal produced by the demo
	@test -f $(JOURNAL) || { echo "no journal at $(JOURNAL) — run 'make demo' and send a command first"; exit 1; }
	./$(BINARY) verify $(JOURNAL)

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)
