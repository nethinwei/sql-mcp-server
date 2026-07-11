GO ?= go
VERSION ?= dev
VERSION_PACKAGE := github.com/nethinwei/sql-mcp-server/version.value
LDFLAGS ?= -X $(VERSION_PACKAGE)=$(VERSION)
BINARY := sql-mcp-server$(shell $(GO) env GOEXE)
CORE_COVERAGE_MIN := 80.0
CORE_PACKAGES := ./core/...

.PHONY: fmt fmt-check vet build test test-integration test-e2e lint coverage \
	coverage-check govulncheck ci ci-local ci-full tidy

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
	golangci-lint run ./...

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
