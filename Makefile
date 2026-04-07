.PHONY: build run mcp tidy lint test migrate-up migrate-down

build:
	go build ./...

# Run the HTTP server (phase 2)
run:
	go run ./cmd/server

# Run the MCP server over stdio (Claude Code integration)
mcp:
	go run ./cmd/server mcp

tidy:
	go mod tidy

lint:
	golangci-lint run ./...

test:
	go test ./... -race

# Convenience targets for goose outside of the binary
migrate-up:
	goose -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir migrations postgres "$(DATABASE_URL)" down
