.PHONY: help fmt test vet build verify compose-config compose-up compose-down migrate run-api run-worker

help:
	@echo "KnowFlow development targets"
	@echo "  make verify          Format, test, vet, build, and validate Compose"
	@echo "  make compose-up      Build and start the development stack"
	@echo "  make migrate         Re-run versioned migrations in Compose"
	@echo "  make run-api         Run API with the current environment"
	@echo "  make run-worker      Run Worker with the current environment"

fmt:
	gofmt -w $$(find cmd internal migrations -name '*.go')

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./...

compose-config:
	docker compose config --quiet

verify: fmt test vet build compose-config

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

migrate:
	docker compose run --rm api /usr/local/bin/migrate

run-api:
	go run ./cmd/api

run-worker:
	go run ./cmd/worker
