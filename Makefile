.PHONY: build build-fdb run migrate tidy lint test

build:
	go build ./...

# Production build: includes real FoundationDB adapter.
# Requires FDB C headers (apt install foundationdb-clients on Linux).
build-fdb:
	CGO_ENABLED=1 go build -tags fdb -trimpath -ldflags="-s -w" -o bin/server ./cmd/server

run:
	go run ./cmd/server

migrate:
	go run ./cmd/server migrate

tidy:
	go mod tidy

lint:
	golangci-lint run ./...

test:
	go test ./... -race
