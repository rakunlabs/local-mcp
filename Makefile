PROJECT   := local
MAIN_FILE := cmd/local/main.go

BUILD_DATE   := $(shell date -u '+%Y-%m-%d_%H:%M:%S')
BUILD_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo -)
VERSION      := $(or $(IMAGE_TAG),$(shell git describe --tags --first-parent --match "v*" 2> /dev/null || echo v0.0.0))
LDFLAGS      := -s -w -X main.version=$(VERSION) -X main.commit=$(BUILD_COMMIT) -X main.date=$(BUILD_DATE)

.DEFAULT_GOAL := help

.PHONY: test
test: ## Run unit tests
	go test -race -cover ./...

.PHONY: lint
lint: ## Run go vet and check formatting
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

.PHONY: build
build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(PROJECT) $(MAIN_FILE)

.PHONY: install
install: ## Install the binary into GOBIN
	CGO_ENABLED=0 go install -trimpath -ldflags="$(LDFLAGS)" ./cmd/local

.PHONY: run
run: ## Run over stdio
	go run -ldflags="$(LDFLAGS)" $(MAIN_FILE)

.PHONY: run-server
run-server: ## Run over streamable HTTP
	go run -ldflags="$(LDFLAGS)" $(MAIN_FILE) --server

.PHONY: help
help: ## Display this help screen
	@grep -h -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'
