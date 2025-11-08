.PHONY: help build test run-example docker-db migrate clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build the project
	go build -v ./...

test: ## Run tests
	go test -v ./...

run-example: ## Run the order example
	go run examples/order/main.go

docker-db: ## Start PostgreSQL 18 in Docker
	docker run --name flows-postgres \
		-e POSTGRES_PASSWORD=postgres \
		-e POSTGRES_DB=flows \
		-p 5432:5432 \
		-d postgres:18

migrate: ## Run database migrations
	psql -h localhost -U postgres -d flows -f migrations/001_init.sql

stop-db: ## Stop PostgreSQL Docker container
	docker stop flows-postgres
	docker rm flows-postgres

clean: ## Clean build artifacts
	go clean
	rm -rf bin/

fmt: ## Format code
	go fmt ./...

vet: ## Run go vet
	go vet ./...

deps: ## Download dependencies
	go mod download
	go mod tidy

.DEFAULT_GOAL := help
