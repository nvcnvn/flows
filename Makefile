.PHONY: help build test test-coverage docker-up docker-down docker-clean clean lint fmt deps ci-test ci-lint

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build the project
	go build -v ./...

docker-up: ## Start PostgreSQL 18 using Docker Compose
	docker-compose up -d
	@echo "Waiting for PostgreSQL to be ready..."
	@sleep 5

docker-down: ## Stop Docker Compose services
	docker-compose down

docker-clean: ## Remove Docker volumes
	docker-compose down -v

test: docker-up ## Run all tests
	@export DATABASE_URL="postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable" && \
	go test -v -race ./...

test-coverage: docker-up ## Run tests with coverage
	@export DATABASE_URL="postgres://postgres:postgres@localhost:5433/flows_test?sslmode=disable" && \
	go test -v -race -coverpkg=./... -coverprofile=coverage.out -covermode=atomic ./... && \
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
	go test -v -race -coverpkg=./... -coverprofile=coverage.txt -covermode=atomic ./...

ci-lint: ## Run linter in CI
	golangci-lint run --timeout=5m

clean: docker-down ## Clean up build artifacts
	rm -f coverage.out coverage.html
	go clean -testcache

.DEFAULT_GOAL := help
