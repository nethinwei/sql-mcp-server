# Contributing to sql-mcp-server

This document records the coding standard and contribution flow. The standard is
based on the [Google Go Style Guide](https://google.github.io/styleguide/go/).
Read it before touching a core package.

## Architecture boundary (machine-enforced)

Core packages (`config`, `relalg`, `codegen`, `entity`, `dialect`, `store`,
`rbac`, `mask`, `cost`, `audit`, `tool`, `cache`, `hook`, `ratelimit`,
`engine`, `introspect`) depend **only on the standard library and each other**.
They must never import third-party packages or anything under `x/`. External
dependencies (the MCP SDK, database drivers, OpenTelemetry, YAML) live under
`x/` and depend on core, never the reverse.

This is enforced by `depguard` in `.golangci.yml` (strict allow-list with
`$gostd`). The CI `lint` job fails on any violation.

## Code style

### Formatting and tools

- `gofmt` is the only formatting standard. Run `gofmt -w .` before committing.
- `golangci-lint` default rule set plus `depguard`; core packages must have zero
  lint warnings.
- CI runs `go test -race ./...` and `govulncheck`.

### Naming

- Packages: lower-case words, no underscores or camelCase (`relalg`, `cost`).
- Exported symbols: PascalCase (`NewGate`, `ScanFull`, `ActionRead`).
- Unexported: camelCase (`scorePlan`, `compileSelect`).
- Error variables: `Err` prefix (`ErrCostExceeded`, `ErrOverloaded`).
- Option functions: `With` prefix (`WithThreshold`, `WithDialect`).
- Interfaces: simple nouns (`Dialect`, `Explainer`, `Gate`); never `IDialect`
  or `ToolInterface`.
- Initialisms: all-caps (`HTTP`, `JSON`, `ID`, `DSN`, `SQL`).

### Public API pattern

Every public constructor takes required positional parameters plus options and
returns an error:

```go
func NewGate(explainer cost.Explainer, opts ...Option) (*Gate, error)
```

Rules:

- Required values are explicit positional parameters; optional behavior uses
  `WithXxx()`.
- Options never hide required parameters.
- Constructors validate input and return an error; they never panic.
- No `MustNew` or `NewDefault` variants.

An option is `type Option func(*X)`; apply nil options safely:

```go
for _, opt := range opts {
    if opt != nil { opt(x) }
}
```

### Error handling

Sentinel errors at package level with a doc comment:

```go
// ErrCostExceeded is returned when a query's estimated cost exceeds the
// configured threshold.
var ErrCostExceeded = errors.New("query cost exceeded threshold")
```

Context-carrying errors are structs with `Unwrap`:

```go
type CostExceededError struct {
    Plan      Plan
    Score     Score
    Threshold Threshold
    Hints     []string
    Soft      bool
}

func (e *CostExceededError) Error() string { /* ... */ }
func (e *CostExceededError) Unwrap() error { return ErrCostExceeded }
```

Wrap with `fmt.Errorf("%w: ...", err)` to keep `errors.Is`/`errors.As` working.
Never discard an error with `_` unless a comment explains why.

### Functions and control flow

- Prefer options over boolean positional flags: `WithMaxRows(1000)` beats
  `New(g, 1000, true)`.
- Return early; minimize nesting.
- Use `if` init statements: `if err := ctx.Err(); err != nil { ... }`.
- A function body is at most 50 lines; split if longer.

### Comments

- Exported identifiers need a doc comment starting with the identifier name.
- Package comments live in `doc.go`.
- Describe what and why, not how.
- Do not repeat information already in the signature.

### File organization

- A source file is at most 800 lines; split by responsibility, not to hit a
  line count. (`cost/` splits into `plan.go`, `gate.go`, `score.go`, `hint.go`.)

### Dependencies

- Core packages: standard library and sibling core packages only. No
  third-party imports.
- `x/` packages may import third-party SDKs and core.

### Testing

- Tests live in the same package (`_test.go`, white-box) and use unexported
  types. Executable examples use a `xxx_test` package (black-box).
- TDD: write a failing test, implement, watch it pass.
- Names describe behavior: `TestGateRejectsFullScan`, never `TestGateUnit`.
- Table-driven tests with `t.Run` when only inputs differ.
- Hand-written fakes implement `store.DB`/`Explainer`/`Authorizer`. No
  `testify`, no `mockgen`.
- Property-based tests verify the core invariants (I1–I13).
- CI runs `go test -race ./...`.

### Data model

- Flat discriminated unions. `relalg.Expr` is a sealed `interface{ rel() }`;
  implementations hold an unexported `rel()` method so external types cannot
  satisfy it.
- Capability tags use interface type assertions (`CostGated`), not metadata
  strings.

## Core invariants (verified by property tests)

- **I1** Core packages import only stdlib and core.
- **I2** Dependency is one-way: `x/ -> core`.
- **I3** All generated SQL parameterizes user values via `Placeholder`; no
  string concatenation.
- **I4** A `CostGated` tool executes only after `Gate.Check` passes.
- **I5** `Gate.Check` runs only after `Authorizer.Authorize` allows.
- **I6** Every returned field is in `Decision.Fields`.
- **I7** The effective predicate is `user_predicate AND role_row_filter`.
- **I8** A tool is registered iff `Enabled(flags) && entity.MCP.DMLTools`.
- **I9** `Engine`/`Registry` hold no per-request mutable state; safe for
  concurrent reuse.
- **I10** Every `Rows`/`Tx` is closed/committed/rolled back (`defer`).
- **I11** Context cancellation cancels the DB query (`Canceler`) and reclaims
  goroutines.
- **I12** `Auditor.Record` never blocks the main flow.
- **I13** `in-flight > limit => ErrOverloaded`; no goroutine pile-up.
- **I14** A non-PK-point write (UPDATE/DELETE) is rejected by WriteGuard when
  `requirePKForWrite` is set.
- **I15** filter/group-by/set/values field names must be visible entity
  attributes; a hidden column can be neither a predicate nor a write target.

## Commit flow

1. `gofmt -w .`
2. `go vet ./...`
3. `go test -race ./...` (and `make test-integration`/`make test-e2e` if you
   touched `x/`)
4. `make lint` (includes depguard boundary check)
5. Commit message follows [Conventional Commits](https://www.conventionalcommits.org/):
   English, imperative, first line at most 72 chars — e.g.
   `feat(cost): add dual-threshold gate`.
6. One PR per concern; `main` is protected.
7. Do not auto-commit; human confirmation is required.
