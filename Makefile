.PHONY: build test lint fmt tidy

build:
	go build -o bin/fi-mcp-gateway ./cmd/fi-mcp-gateway

test:
	CGO_ENABLED=1 go test -race ./...

lint:
	@which golangci-lint > /dev/null || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.8.0
	golangci-lint run ./...

fmt:
	gofmt -w $$(git ls-files "*.go")

tidy:
	go mod tidy
