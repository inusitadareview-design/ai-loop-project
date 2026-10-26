# AI Loop Project - Build System
# See .ai/ARCHITECTURE.md for the full project layout.
#
# RULES AND INVARIANTS (from .ai/RULES_AND_INVARIANTS.md):
#   R2 - Quality gates (fmt, vet, test) are mandatory before every commit.
#   G2.2 - Runtime writes ONLY inside data/. Never commit data/.
#   G6.2 - Binaries go to bin/, never versioned.
#   G5.1 - Errors are first-class citizens.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# --- Variables (override via `make VAR=value`) ---
GO           ?= go
GOFMT        ?= gofmt
GOVET        ?= $(GO) vet
GOTEST       ?= $(GO) test
GOBUILD      ?= $(GO) build

MODULE       := github.com/SEU_USUARIO/ai-loop-project
BINDIR       := bin
BINARY       := $(BINDIR)/ailoop
CMD          := ./cmd/ailoop

GO_FILES     := $(shell find . -type f -name '*.go' -not -path "./vendor/*" -not -path "./bin/*")
GO_PACKAGES  := ./...

# --- Phony targets ---
.PHONY: all help deps build test test-race test-cover vet fmt lint clean clean-all build-rust clean-rust

# =========================================================================
# Default and discovery
# =========================================================================

all: fmt vet test build ## Run the full quality gate and build pipeline

help: ## Show this help message
	@echo "AI Loop Project - available targets:"
	@echo
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'
	@echo

# =========================================================================
# Dependencies
# =========================================================================

deps: ## Download and tidy Go module dependencies
	$(GO) mod download
	$(GO) mod tidy

# =========================================================================
# Build
# =========================================================================

build: ## Build the Go binary into $(BINDIR)/ailoop (calls scripts/build.sh)
	@./scripts/build.sh

# =========================================================================
# Quality gates (mandatory before every commit — R2)
# =========================================================================

fmt: ## Run gofmt on all .go files (in-place, simplify)
	@test -z "$(shell $(GOFMT) -l -s $(GO_FILES))" || { \
		echo "Files not formatted:"; $(GOFMT) -l -s $(GO_FILES); \
		$(GOFMT) -w -s $(GO_FILES); \
	}

vet: ## Run go vet on all packages
	$(GOVET) $(GO_PACKAGES)

lint: ## Run golangci-lint (optional — requires external install)
	@command -v golangci-lint >/dev/null 2>&1 || \
		{ echo "golangci-lint not installed. Install from https://golangci-lint.run"; exit 1; }
	golangci-lint run $(GO_PACKAGES)

# =========================================================================
# Tests
# =========================================================================

test: ## Run all Go tests
	$(GOTEST) $(GO_PACKAGES)

test-race: ## Run all Go tests with race detector (mandatory for CI)
	$(GOTEST) -race $(GO_PACKAGES)

test-cover: ## Run tests with coverage report (outputs coverage.html)
	$(GOTEST) -race -coverprofile=coverage.out $(GO_PACKAGES)
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# =========================================================================
# Rust FFI (placeholder — real implementation in Sprint 2)
# =========================================================================

build-rust: ## Build Rust FFI library (placeholder)
	@if [ -d rust/ffi ]; then \
		echo "[rust] building release library..."; \
		cd rust/ffi && cargo build --release; \
	else \
		echo "[rust] rust/ffi/ not found — skipping"; \
	fi

clean-rust: ## Clean Rust build artifacts (placeholder)
	@if [ -d rust/ffi ]; then \
		echo "[rust] cleaning artifacts..."; \
		cd rust/ffi && cargo clean; \
	fi

# =========================================================================
# Cleanup
# =========================================================================

clean: ## Remove Go build artifacts ONLY (keeps data/ — guardrail G2.2)
	rm -rf $(BINDIR)
	rm -f coverage.out coverage.html

clean-all: ## Remove build artifacts AND runtime data (DESTRUCTIVE — prompts for confirmation)
	@echo "WARNING: this will remove $(BINDIR)/ AND data/ (all logs, cache, memories, outputs)."
	@read -p "Are you sure? [y/N] " confirm && [ "$$confirm" = "y" ] || { echo "Aborted."; exit 1; }
	rm -rf $(BINDIR) data/
	rm -f coverage.out coverage.html