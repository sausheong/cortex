BINARY_DIR := bin

.PHONY: all build clean test test-v test-cover vet tidy install run-mcp run-mcp-http

all: build

build:
	@mkdir -p $(BINARY_DIR)
	go build -o $(BINARY_DIR)/cortex ./cmd/cortex/
	go build -o $(BINARY_DIR)/cortex-mcp ./cmd/cortex-mcp/

test:
	go test ./... -count=1

test-v:
	go test ./... -count=1 -v

test-cover:
	go test ./... -count=1 -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(BINARY_DIR)
	rm -f coverage.out coverage.html

install: build
	cp $(BINARY_DIR)/cortex $(BINARY_DIR)/cortex-mcp /usr/local/bin/

run-mcp: build
	$(BINARY_DIR)/cortex-mcp

run-mcp-http: build
	$(BINARY_DIR)/cortex-mcp --transport http
