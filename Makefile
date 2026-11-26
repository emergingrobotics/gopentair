# gopentair Makefile

CLI_BINARY := pentair
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)"

GOCMD := go

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help message
	@echo "gopentair - Pentair Home cloud SDK and CLI"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-15s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: build
build: ## Build the pentair CLI
	$(GOCMD) build $(LDFLAGS) -o $(CLI_BINARY) ./cmd/pentair

.PHONY: build-examples
build-examples: ## Build all examples into bin/
	@mkdir -p bin
	@for d in examples/*/; do n=$$(basename $$d); $(GOCMD) build -o bin/$$n ./$$d || exit 1; done

.PHONY: install
install: ## Install the CLI into GOBIN
	$(GOCMD) install $(LDFLAGS) ./cmd/pentair

.PHONY: test
test: ## Run all tests
	$(GOCMD) test ./...

.PHONY: test-race
test-race: ## Run tests with the race detector
	$(GOCMD) test -race ./...

.PHONY: test-cover
test-cover: ## Run tests with a coverage report
	$(GOCMD) test -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

.PHONY: test-e2e
test-e2e: ## Read-only tests against the real cloud (needs PENTAIR_USERNAME/PASSWORD)
	PENTAIR_E2E=1 $(GOCMD) test -count=1 -run E2E -v .

.PHONY: fmt
fmt: ## Format code
	$(GOCMD) fmt ./...

.PHONY: vet
vet: ## Run go vet
	$(GOCMD) vet ./...

.PHONY: lint
lint: fmt vet ## Format and vet

.PHONY: tidy
tidy: ## Tidy go.mod
	$(GOCMD) mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(CLI_BINARY) coverage.out coverage.html
	rm -rf bin

.PHONY: all
all: clean lint build build-examples test ## Clean, lint, build, test
