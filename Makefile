.PHONY: help fmt lint test build web-install web-test verify compose-config compose-up compose-down migrate smoke eval real-eval-provision real-eval run-api run-worker

help:
	@echo "KnowFlow development targets"
	@echo "  make verify          Format, test, vet, build, and validate Compose"
	@echo "  make compose-up      Build and start the development stack"
	@echo "  make migrate         Re-run versioned migrations in Compose"
	@echo "  make eval            Generate deterministic M5 regression reports"
	@echo "  make real-eval-provision  Upload and index the real-world-v1 corpus"
	@echo "  make real-eval       Run all real-world-v1 retrieval strategies"
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

real-eval-provision:
	go run ./cmd/realworldeval --phase provision --timeout 45m

real-eval:
	go run ./cmd/realworldeval --phase evaluate --timeout 4h

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker
