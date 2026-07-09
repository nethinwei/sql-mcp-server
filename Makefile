GO ?= go

.PHONY: fmt fmt-check vet test test-integration test-e2e lint coverage ci tidy clean

# Format all Go sources in place.
fmt:
	$(GO) fmt ./...

# Fail if any Go source is not gofmt-ed.
fmt-check:
	@unformatted=$$(gofmt -l . 2>/dev/null); \
	if [ -n "$$unformatted" ]; then \
		echo "These files are not gofmt-ed:"; echo "$$unformatted"; exit 1; \
	fi

vet:
	$(GO) vet ./...

# Unit tests (core packages, no docker).
test:
	$(GO) test -race ./...

# Integration tests (real DBs via testcontainers).
test-integration:
	$(GO) test -race -tags=integration ./x/providers/...

# End-to-end tests (real DB + MCP client).
test-e2e:
	$(GO) test -race -tags=e2e ./x/mcpserver/... ./cmd/...

lint:
	golangci-lint run ./...

coverage:
	$(GO) test -race -coverprofile=coverage.txt -covermode=atomic ./...
	$(GO) tool cover -func=coverage.txt | tail -1

# Full local CI mirror.
ci: fmt-check vet lint test coverage

tidy:
	$(GO) mod tidy

clean:
	rm -f coverage.txt coverage.html
