.PHONY: help fmt lint test build web-install web-test verify compose-config compose-up compose-down migrate smoke eval run-api run-worker

help:
	@echo "KnowFlow development targets"
	@echo "  make verify          Format, test, vet, build, and validate Compose"
	@echo "  make compose-up      Build and start the development stack"
	@echo "  make migrate         Re-run versioned migrations in Compose"
	@echo "  make eval            Generate four-strategy JSON and Markdown evaluation reports"
	@echo "  make smoke           Run register-to-cited-answer acceptance flow"
	@echo "  make run-api         Run API with the current environment"
	@echo "  make run-worker      Run Worker with the current environment"

fmt:
	gofmt -w $$(find cmd internal migrations -name '*.go')

test:
	go test ./cmd/... ./internal/... ./migrations

lint:
	go vet ./cmd/... ./internal/... ./migrations
	cd web && npm run lint

build:
	go build ./cmd/... ./internal/... ./migrations
	cd web && npm run build

web-install:
	cd web && npm ci

web-test:
	cd web && npm test

compose-config:
	docker compose config --quiet

verify: fmt lint test web-test build compose-config

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

migrate:
	docker compose run --rm api /usr/local/bin/migrate

smoke:
	docker compose run --rm --build smoke

eval:
	docker compose run --rm --build eval

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker
