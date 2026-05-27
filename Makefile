BINARY_DIR   := bin
DIST_DIR     := $(BINARY_DIR)/dist
PLATFORMS    := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64 windows-arm64
DIST_TARGETS := $(addprefix dist-,$(PLATFORMS))

.PHONY: all build clean test test-v test-cover vet tidy install run-mcp run-mcp-http dist $(DIST_TARGETS)

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

dist: $(DIST_TARGETS)
	@echo "Release artifacts written to $(DIST_DIR)/"
	@ls -1 $(DIST_DIR)/

$(DIST_TARGETS):
	@mkdir -p $(DIST_DIR)
	@OS=`echo $@ | sed 's/^dist-//' | cut -d- -f1`; \
	ARCH=`echo $@ | sed 's/^dist-//' | cut -d- -f2`; \
	EXT=`[ "$$OS" = "windows" ] && echo .exe || echo ""`; \
	echo "Building $$OS/$$ARCH..."; \
	CGO_ENABLED=0 GOOS=$$OS GOARCH=$$ARCH go build -o $(DIST_DIR)/cortex-$$OS-$$ARCH$$EXT     ./cmd/cortex/     && \
	CGO_ENABLED=0 GOOS=$$OS GOARCH=$$ARCH go build -o $(DIST_DIR)/cortex-mcp-$$OS-$$ARCH$$EXT ./cmd/cortex-mcp/
