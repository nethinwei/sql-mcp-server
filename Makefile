GO ?= go
GORELEASER ?= goreleaser
ACTIONLINT ?= actionlint
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION ?= 2.12.2
MCP_PUBLISHER ?= mcp-publisher
PYTHON ?= python3
SYFT ?= syft
VERSION ?= dev
RELEASE_VERSION ?= 0.1.3
RELEASE_IMAGE ?= sql-mcp-server:release-preflight
VERSION_PACKAGE := github.com/nethinwei/sql-mcp-server/version.value
LDFLAGS ?= -X $(VERSION_PACKAGE)=$(VERSION)
BINARY := sql-mcp-server$(shell $(GO) env GOEXE)
CORE_COVERAGE_MIN := 80.0
CORE_PACKAGES := ./core/...

.PHONY: fmt fmt-check vet build test test-integration test-e2e lint coverage \
	coverage-check govulncheck workflow-check release-check release-quality \
	release-snapshot release-metadata-check release-image-check release-preflight-fast \
	release-preflight ci ci-local ci-full tidy

# Format all Go sources in place (gofmt + 120-column line shortening).
fmt:
	$(GO) run ./internal/fmtcheck -w

# Fail if any Go source is not gofmt-ed.
fmt-check:
	$(GO) run ./internal/fmtcheck

vet:
	$(GO) vet ./...

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/sql-mcp-server

release-check:
	$(GORELEASER) check

workflow-check:
	$(ACTIONLINT)

release-quality: fmt-check vet test coverage-check

release-snapshot: release-check
	PATH="$$(dirname "$$(command -v $(SYFT))"):$$PATH" \
		$(GORELEASER) release --snapshot --clean --skip=publish,sign
	scripts/release/verify-dist.sh dist

release-metadata-check:
	$(PYTHON) scripts/release/metadata.py render --version $(RELEASE_VERSION) \
		--file server.json --output dist/server.json
	$(MCP_PUBLISHER) validate dist/server.json

release-image-check:
	docker build --build-arg VERSION=v$(RELEASE_VERSION) -t $(RELEASE_IMAGE) .
	scripts/release/verify-image-label.sh $(RELEASE_IMAGE)
	$(SYFT) $(RELEASE_IMAGE) --output spdx-json=dist/image.spdx.json
	SQL_MCP_IMAGE=$(RELEASE_IMAGE) scripts/release/quickstart.sh

release-preflight-fast:
	$(MAKE) workflow-check
	$(MAKE) release-snapshot
	$(MAKE) release-metadata-check
	$(MAKE) release-image-check

release-preflight:
	$(MAKE) ci-full
	$(MAKE) release-preflight-fast

# Unit tests (core packages, no docker).
test:
	$(GO) test -race ./...

# Integration tests (real DBs via testcontainers; Docker required).
test-integration:
	$(GO) test -race -tags=integration -timeout 20m ./x/providers/...

test-integration-%:
	$(GO) test -race -tags=integration -timeout 20m ./x/providers/$*/...

# End-to-end tests (real DB + MCP client).
test-e2e:
	$(GO) test -race -tags=e2e -timeout 10m ./x/mcpserver/...

lint:
	@version="$$($(GOLANGCI_LINT) version)"; echo "$$version"; \
		case "$$version" in *"version $(GOLANGCI_LINT_VERSION) "*) ;; \
		*) echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required" >&2; exit 1 ;; esac
	$(GOLANGCI_LINT) run ./...

coverage:
	$(GO) test -coverprofile=coverage.txt -covermode=atomic $(CORE_PACKAGES)

coverage-check: coverage
	$(GO) run ./internal/coveragecheck -profile coverage.txt -min $(CORE_COVERAGE_MIN)

govulncheck:
	govulncheck ./...

# Docker-free local release gates. Install golangci-lint and govulncheck first.
ci: ci-local
ci-local: fmt-check vet lint build test coverage-check govulncheck

# Complete release gates; additionally requires Docker for three providers/e2e.
ci-full: ci-local test-integration test-e2e

tidy:
	$(GO) mod tidy
