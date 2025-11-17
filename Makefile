.PHONY: help build test test-coverage docker-up docker-down docker-clean clean lint fmt deps ci-test ci-lint

# Detect Docker Compose (prefers v1 binary, falls back to v2 plugin)
DOCKER_COMPOSE ?= $(shell \
	if command -v docker-compose >/dev/null 2>&1; then \
		echo docker-compose; \
	elif docker compose version >/dev/null 2>&1; then \
		echo "docker compose"; \
	fi)

ifeq ($(strip $(DOCKER_COMPOSE)),)
$(error Docker Compose not found. Install docker-compose or enable the Docker CLI compose plugin.)
endif

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build the project
	go build -v ./...

docker-up: ## Start PostgreSQL 18 using Docker Compose
	$(DOCKER_COMPOSE) up -d
	@echo "Waiting for PostgreSQL to be ready..."
	@sleep 5

docker-down: ## Stop Docker Compose services
	$(DOCKER_COMPOSE) down

docker-clean: ## Remove Docker volumes
	$(DOCKER_COMPOSE) down -v

test:
	@export DATABASE_URL="postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable" && \
	go test -v -race -parallel 8 -count 1 ./...

test-coverage:
	@export DATABASE_URL="postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable" && \
	go test -v -race -parallel 8 -coverpkg=./... -coverprofile=coverage.out -covermode=atomic ./... && \
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

lint: ## Run linter
	golangci-lint run --timeout=5m

fmt: ## Format code
	go fmt ./...
	goimports -w .

deps: ## Download dependencies
	go mod download
	go mod tidy

# CI targets
ci-test: ## Run tests in CI environment
	go test -v -race -parallel 4 -coverpkg=./... -coverprofile=coverage.txt -covermode=atomic ./...

ci-lint: ## Run linter in CI
	golangci-lint run --timeout=5m

clean: docker-down ## Clean up build artifacts
	rm -f coverage.out coverage.html
	go clean -testcache

.DEFAULT_GOAL := help
