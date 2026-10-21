# Optional local environment (gitignored); see .env.example for the fields.
-include .env
export EVAL_API_KEY EVAL_MODEL EVAL_BASE_URL EVAL_PARALLEL \
	OTEL_EXPORTER_OTLP_ENDPOINT BENCH_ROWS BENCH_ITERATIONS

GO ?= go
GORELEASER ?= goreleaser
ACTIONLINT ?= actionlint
GOLANGCI_LINT ?= golangci-lint
GOLANGCI_LINT_VERSION ?= 2.12.2
# golangci-lint type-checks against the running toolchain's standard library;
# pin it to go.mod's toolchain so a newer local Go does not break lint.
LINT_GOTOOLCHAIN ?= $(shell sed -n 's/^toolchain //p' go.mod)
MCP_PUBLISHER ?= mcp-publisher
PYTHON ?= python3
SYFT ?= syft
VERSION ?= dev
# The release being prepared is server.json's; release-bump takes the next
# one explicitly.
RELEASE_VERSION ?= $(shell $(PYTHON) -c 'import json; print(json.load(open("server.json"))["version"])')
# release-version-named fails a target unless RELEASE_VERSION was named (on
# the command line or in the environment): server.json's own would only check
# the file against itself.
release-version-named = @test "$(origin RELEASE_VERSION)" != file || \
	{ echo "$@: name the version: make $@ RELEASE_VERSION=x.y.z" >&2; exit 1; }
RELEASE_IMAGE ?= sql-mcp-server:release-preflight
QUICKSTART_IMAGE ?= sql-mcp-server:quickstart-check
VERSION_PACKAGE := github.com/nethinwei/sql-mcp-server/version.value
LDFLAGS ?= -X $(VERSION_PACKAGE)=$(VERSION)
BINARY := sql-mcp-server$(shell $(GO) env GOEXE)
CORE_COVERAGE_MIN := 80.0
CORE_PACKAGES := ./core/...

.PHONY: fmt web fmt-check vet build test test-fuzz-smoke test-integration test-e2e lint coverage \
	coverage-check govulncheck workflow-check release-check release-quality \
	release-snapshot release-metadata-check release-image-check release-preflight-fast \
	release-preflight release-bump quickstart-check modelscope-check smoke-protocol bench bench-integration bench-overhead eval-pilot \
	eval-workload fixtures-v4 eval-diagnostic eval-coverage docs-check ci ci-local ci-full tidy web-check

# Format all Go sources in place (gofmt + 120-column line shortening).
fmt:
	$(GO) run ./internal/fmtcheck -w

# Admin console (web/admin): installs locked dependencies, regenerates GraphQL
# types (gitignored, needed by the tests), tests, type-checks and builds into
# x/admin/ui/dist/app for go:embed.
web:
	cd web/admin && pnpm install --frozen-lockfile && pnpm codegen && pnpm lint && pnpm test && pnpm build

# Fail unless the built console is in place for go:embed (release builds).
web-check:
	@test -f x/admin/ui/dist/app/index.html || \
		{ echo "admin console not built: run make web before building a release" >&2; exit 1; }

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

# Rewrite all pinned version references (server.json, examples, README GA
# lines) in one idempotent pass; editorial files stay manual.
release-bump:
	$(release-version-named)
	$(PYTHON) scripts/release/bump.py --version $(RELEASE_VERSION)

# The checked-in server.json must already name RELEASE_VERSION (server and
# package versions, OCI tag): verify it before rendering the release copy.
# Name the version being released (make release-preflight-fast
# RELEASE_VERSION=x.y.z); left unset it fails. CI names server.json's own to
# check the file's form; the tag workflow verifies the tag.
release-metadata-check:
	$(release-version-named)
	$(PYTHON) scripts/release/metadata.py verify --version $(RELEASE_VERSION) --file server.json
	$(PYTHON) scripts/release/metadata.py render --version $(RELEASE_VERSION) \
		--file server.json --output dist/server.json
	$(MCP_PUBLISHER) validate dist/server.json

release-image-check:
	docker build --build-arg VERSION=v$(RELEASE_VERSION) -t $(RELEASE_IMAGE) .
	scripts/release/verify-image-label.sh $(RELEASE_IMAGE)
	$(SYFT) $(RELEASE_IMAGE) --output spdx-json=dist/image.spdx.json
	SQL_MCP_IMAGE=$(RELEASE_IMAGE) scripts/release/quickstart.sh

# The quickstart allow/deny smoke against an image built from this checkout
# (Docker required): in CI and before a release publishes anything, so a
# regression is caught before the published-image check after the push.
quickstart-check:
	docker build -t $(QUICKSTART_IMAGE) .
	SQL_MCP_IMAGE=$(QUICKSTART_IMAGE) scripts/release/quickstart.sh

modelscope-check: build
	MODELSCOPE_BINARY=./$(BINARY) $(GO) run ./internal/modelscopesmoke

# Protocol smoke: stdio + streamable HTTP against a real database (Docker required).
smoke-protocol: build
	PROTOCOL_BINARY=./$(BINARY) $(GO) run ./internal/protocolsmoke

# Go benchmarks of the hot paths, no Docker (see docs/benchmarks/hot-paths.md).
bench:
	$(GO) test -run '^$$' -bench . -benchmem ./core/... ./x/...

# Metadata scans and privilege probes against real databases (Docker required).
bench-integration:
	$(GO) test -tags=integration -run '^$$' -bench . -benchtime 20x ./x/providers/postgres ./x/providers/mysql

# Data-plane overhead benchmark (Docker required). See docs/benchmarks/.
# Recipes are silenced (@) so stdout stays parseable JSON.
bench-overhead:
	@$(GO) run ./internal/benchoverhead

# Agent Eval regression track: frozen v3 task set (Docker + EVAL_API_KEY/EVAL_MODEL
# required). See eval/README.md.
eval-pilot:
	@$(GO) run ./eval/runner

# Agent Eval realistic-workload track: fixtures/v4 business workload (Docker +
# EVAL_API_KEY/EVAL_MODEL required; EVAL_DSN/EVAL_CONFIG/EVAL_TASKS switch to
# dogfooding mode against an external database). See eval/README.md.
eval-workload:
	@$(GO) run ./eval/runner -track workload

# Agent Eval diagnostic track: fixtures/v4/tasks-v5 (v0.1.10).
eval-diagnostic:
	@$(GO) run ./eval/runner -track diagnostic

# Generate fixtures/v4/coverage/matrix.json from tasks-v5 metadata.
eval-coverage:
	@$(GO) run ./cmd/evalcoverage

# Regenerate the checked-in fixtures/v4 workload artifacts from the
# deterministic generator (a drift test locks them to the generator).
fixtures-v4:
	$(GO) run ./fixtures/v4/generator/cmd/workloadgen

# Documentation consistency: relative markdown links resolve and pinned
# version references (README, roadmap, releases, CHANGELOG) agree.
docs-check:
	$(GO) run ./internal/docscheck

release-preflight-fast:
	$(release-version-named)
	$(MAKE) workflow-check
	$(MAKE) release-snapshot
	$(MAKE) release-metadata-check
	$(MAKE) release-image-check
	$(MAKE) modelscope-check

release-preflight:
	$(release-version-named)
	$(MAKE) ci-full
	$(MAKE) web web-check
	$(MAKE) smoke-protocol
	$(MAKE) release-preflight-fast

# Unit tests, including fuzz seed corpus replay (no docker).
test:
	$(GO) test -race ./...

# Bounded fuzz smoke (four independent invocations, no docker; about 80 seconds total).
test-fuzz-smoke:
	$(GO) test ./core/tool -run='^$$' -fuzz='^FuzzToolPayloadDecodeNormalizeFieldGate$$' -fuzztime=20s -timeout=30s
	$(GO) test ./core/relalg -run='^$$' -fuzz='^FuzzValidatePredicate$$' -fuzztime=20s -timeout=30s
	$(GO) test ./core/codegen -run='^$$' -fuzz='^FuzzCompileNoInjectionAndQuoting$$' -fuzztime=20s -timeout=30s
	$(GO) test ./core/tool -run='^$$' -fuzz='^FuzzTransactionStateMachine$$' -fuzztime=20s -timeout=30s

# Integration tests (real DBs via testcontainers; Docker required).
test-integration:
	$(GO) test -race -tags=integration -timeout 20m ./x/providers/... ./x/configstore/...

test-integration-%:
	$(GO) test -race -tags=integration -timeout 20m ./x/providers/$*/...
	$(GO) test -race -tags=integration -timeout 20m -run '(?i)$*' ./x/configstore/...

# End-to-end tests (real DB + MCP client).
test-e2e:
	$(GO) test -race -tags=e2e -timeout 10m ./x/mcpserver/...

lint:
	@version="$$($(GOLANGCI_LINT) version)"; echo "$$version"; \
		case "$$version" in *"version $(GOLANGCI_LINT_VERSION) "*) ;; \
		*) echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required" >&2; exit 1 ;; esac
	GOTOOLCHAIN=$(LINT_GOTOOLCHAIN) $(GOLANGCI_LINT) run ./...

coverage:
	$(GO) test -coverprofile=coverage.txt -covermode=atomic $(CORE_PACKAGES)

coverage-check: coverage
	$(GO) run ./internal/coveragecheck -profile coverage.txt -min $(CORE_COVERAGE_MIN)

govulncheck:
	govulncheck ./...

# Docker-free local release gates. Install golangci-lint and govulncheck first.
ci: ci-local
ci-local: fmt-check vet lint docs-check build test coverage-check govulncheck

# Complete release gates; additionally requires Docker for three providers/e2e.
ci-full: ci-local test-integration test-e2e

tidy:
	$(GO) mod tidy
