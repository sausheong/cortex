BINARY_DIR   := bin
DIST_DIR     := $(BINARY_DIR)/dist
PLATFORMS    := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64 windows-amd64 windows-arm64
DIST_TARGETS := $(addprefix dist-,$(PLATFORMS))

PREFIX      ?= /usr/local
INSTALL_DIR := $(PREFIX)/bin

.PHONY: all build clean test test-v test-cover vet tidy install uninstall run-mcp run-mcp-http dist $(DIST_TARGETS) release-archives release release-notes

all: build

build:
	@mkdir -p $(BINARY_DIR)
	go build -o $(BINARY_DIR)/cortex ./cmd/cortex/

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
	@echo "Installing cortex to $(INSTALL_DIR)..."
	@mkdir -p $(INSTALL_DIR)
	@if [ -w $(INSTALL_DIR) ]; then \
		install -m 0755 $(BINARY_DIR)/cortex $(INSTALL_DIR)/cortex; \
	else \
		echo "  (requires sudo for $(INSTALL_DIR))"; \
		sudo install -m 0755 $(BINARY_DIR)/cortex $(INSTALL_DIR)/cortex; \
	fi
	@echo "Installed:"
	@echo "  $(INSTALL_DIR)/cortex"

uninstall:
	@echo "Removing cortex from $(INSTALL_DIR)..."
	@if [ -w $(INSTALL_DIR) ]; then \
		rm -f $(INSTALL_DIR)/cortex; \
	else \
		sudo rm -f $(INSTALL_DIR)/cortex; \
	fi
	@echo "Uninstalled."

run-mcp: build
	$(BINARY_DIR)/cortex mcp

run-mcp-http: build
	$(BINARY_DIR)/cortex mcp --transport http

dist: $(DIST_TARGETS)
	@echo "Release artifacts written to $(DIST_DIR)/"
	@ls -1 $(DIST_DIR)/

$(DIST_TARGETS):
	@mkdir -p $(DIST_DIR)
	@OS=`echo $@ | sed 's/^dist-//' | cut -d- -f1`; \
	ARCH=`echo $@ | sed 's/^dist-//' | cut -d- -f2`; \
	EXT=`[ "$$OS" = "windows" ] && echo .exe || echo ""`; \
	echo "Building $$OS/$$ARCH..."; \
	CGO_ENABLED=0 GOOS=$$OS GOARCH=$$ARCH go build -o $(DIST_DIR)/cortex-$$OS-$$ARCH$$EXT ./cmd/cortex/

# --- Release ---
#
# Usage:
#   make release-archives VERSION=v0.1.0   # build + package, no push
#   make release VERSION=v0.1.0            # all of the above, then tag, push,
#                                          # and create a GitHub release.
#
# Requirements: gh CLI installed and authenticated; clean working tree; tests pass.

release-archives: dist
	@test -n "$(VERSION)" || (echo "ERROR: VERSION is required, e.g. make $@ VERSION=v0.1.0"; exit 1)
	@echo "Packaging archives for $(VERSION)..."
	@./scripts/package-release.sh "$(VERSION)" "$(DIST_DIR)" $(PLATFORMS)
	@echo "Release archives ready in $(DIST_DIR)/"

# Generate release notes from commits since the previous tag.
# Writes to $(DIST_DIR)/RELEASE_NOTES.md.
release-notes:
	@test -n "$(VERSION)" || (echo "ERROR: VERSION is required, e.g. make $@ VERSION=v0.1.0"; exit 1)
	@mkdir -p $(DIST_DIR)
	@PREV=`git tag --sort=-creatordate --list 'v*' | grep -v "^$(VERSION)\$$" | head -1`; \
	OUT=$(DIST_DIR)/RELEASE_NOTES.md; \
	if [ -z "$$PREV" ]; then \
		echo "## $(VERSION) — initial release"  > $$OUT; \
		echo ""                                >> $$OUT; \
		echo "First public release of cortex." >> $$OUT; \
	else \
		echo "## $(VERSION)"                                                                                      > $$OUT; \
		echo ""                                                                                                  >> $$OUT; \
		echo "### Changes since $$PREV"                                                                          >> $$OUT; \
		echo ""                                                                                                  >> $$OUT; \
		git log --pretty=format:'- %s' --no-merges "$$PREV..HEAD"                                                >> $$OUT; \
		echo ""                                                                                                  >> $$OUT; \
		echo ""                                                                                                  >> $$OUT; \
		echo "**Full changelog:** https://github.com/sausheong/cortex/compare/$$PREV...$(VERSION)"               >> $$OUT; \
	fi; \
	echo ""                                                                                                      >> $$OUT; \
	echo "### Verify downloads"                                                                                  >> $$OUT; \
	echo ""                                                                                                      >> $$OUT; \
	echo '```'                                                                                                   >> $$OUT; \
	echo "shasum -a 256 -c SHA256SUMS"                                                                           >> $$OUT; \
	echo '```'                                                                                                   >> $$OUT; \
	echo "Wrote $$OUT"

release: release-archives release-notes
	@test -n "$(VERSION)" || (echo "ERROR: VERSION is required"; exit 1)
	@command -v gh >/dev/null 2>&1 || (echo "ERROR: gh CLI not installed"; exit 1)
	@gh auth status >/dev/null 2>&1 || (echo "ERROR: gh not authenticated; run 'gh auth login'"; exit 1)
	@git diff-index --quiet HEAD -- || (echo "ERROR: working tree has uncommitted changes"; exit 1)
	@echo "Running tests..."
	@$(MAKE) -s test
	@if git rev-parse "$(VERSION)" >/dev/null 2>&1; then \
		echo "Tag $(VERSION) already exists locally."; \
	else \
		echo "Creating annotated tag $(VERSION)..."; \
		git tag -a "$(VERSION)" -m "Release $(VERSION)"; \
	fi
	@echo "Pushing tag $(VERSION) to origin..."
	@git push origin "$(VERSION)"
	@echo "Creating GitHub release $(VERSION)..."
	@gh release create "$(VERSION)" \
		--title "$(VERSION)" \
		--notes-file $(DIST_DIR)/RELEASE_NOTES.md \
		$(DIST_DIR)/cortex-$(VERSION)-*.tar.gz \
		$(DIST_DIR)/cortex-$(VERSION)-*.zip \
		$(DIST_DIR)/SHA256SUMS
	@echo ""
	@echo "Released: https://github.com/sausheong/cortex/releases/tag/$(VERSION)"
