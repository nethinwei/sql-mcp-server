package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/bus"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/engine"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/ratelimit"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

// Denial is the stable, machine-readable rejection contract returned to MCP
// clients for business-level errors. Field names and code values are part of
// the public tool contract: adding a new optional field or a new code is a
// compatible change; renaming or removing either is breaking.
type Denial struct {
	Code        string         `json:"code"`
	Reason      string         `json:"reason"`
	Retryable   bool           `json:"retryable"`
	Constraints map[string]any `json:"constraints,omitempty"`
	Hints       []string       `json:"hints,omitempty"`
	DecisionID  string         `json:"decisionId"`
}

// Stable machine codes for business-level rejections. Retryable means a
// revised request could succeed under the caller's current privileges; hints
// may only tighten or equivalently rewrite the request, never widen it.
const (
	CodeUnauthorized        = "UNAUTHORIZED"
	CodeEntityNotFound      = "ENTITY_NOT_FOUND"
	CodeEntityUnavailable   = "ENTITY_UNAVAILABLE"
	CodeInvalidInput        = "INVALID_INPUT"
	CodeDMLToolsDisabled    = "DML_TOOLS_DISABLED"
	CodeUnsafeWrite         = "UNSAFE_WRITE"
	CodeDatabaseError       = "DATABASE_ERROR"
	CodeCostExceeded        = "COST_EXCEEDED"
	CodeBudgetExceeded      = "BUDGET_EXCEEDED"
	CodeTransactionNotFound = "TRANSACTION_NOT_FOUND"
	CodeTransactionScope    = "TRANSACTION_SCOPE"
	CodeTransactionCapacity = "TRANSACTION_CAPACITY"
	CodeTransactionStale    = "TRANSACTION_STALE"
	CodeAmbiguousFieldScope = "AMBIGUOUS_FIELD_SCOPE"
	CodeConstraintViolation = "CONSTRAINT_VIOLATION"
	// CodeRowPolicyViolation: the row a create or update writes would be
	// outside the caller's row scope (tenant policy, granted rows).
	CodeRowPolicyViolation  = "ROW_POLICY_VIOLATION"
	CodeDatasourceForbidden = "DATASOURCE_FORBIDDEN"
	CodeAmbiguousEntity     = "AMBIGUOUS_ENTITY"
	CodeOutcomeUnknown      = "OUTCOME_UNKNOWN"
	CodeAuditUnavailable    = "AUDIT_UNAVAILABLE"
	CodeRateLimited         = "RATE_LIMITED"
	// CodeOverloaded: the process is at capacity (calls in flight, request
	// rate, execution slots); CodeDatasourceUnavailable: the datasource keeps
	// failing and calls to it are held off for a while (circuit breaker).
	CodeOverloaded            = "OVERLOADED"
	CodeDatasourceUnavailable = "DATASOURCE_UNAVAILABLE"
)

// transient are the errors of a call held back by load or a failing
// database: the caller retries later, with nothing to change.
var transient = []struct {
	errs []error
	code string
	hint string
}{
	{[]error{engine.ErrOverloaded, bus.ErrOverloaded, ratelimit.ErrRateLimited, engine.ErrClosed, bus.ErrClosed},
		CodeOverloaded, "the server is at capacity: retry after a moment"},
	{[]error{ratelimit.ErrCircuitOpen}, CodeDatasourceUnavailable,
		"the datasource keeps failing and is held off: retry in a few seconds"},
}

var sentinelDenials = []struct {
	err       error
	code      string
	retryable bool
}{
	{ErrUnauthorized, CodeUnauthorized, false},
	{ErrEntityNotFound, CodeEntityNotFound, false},
	{ErrEntityUnavailable, CodeEntityUnavailable, false},
	{ErrInvalidInput, CodeInvalidInput, true},
	{relalg.ErrINCardinality, CodeInvalidInput, true}, // a list past the codegen bound
	{ErrDMLToolsDisabled, CodeDMLToolsDisabled, false},
	{ErrUnsafeWrite, CodeUnsafeWrite, true},
	{ErrDatasourceForbidden, CodeDatasourceForbidden, false},
	{ErrDatabase, CodeDatabaseError, false},
	{ErrTransactionNotFound, CodeTransactionNotFound, false},
	{ErrTransactionScope, CodeTransactionScope, false},
	{ErrTransactionCapacity, CodeTransactionCapacity, true},
	{ErrTransactionStale, CodeTransactionStale, false},
	{ErrAuditUnavailable, CodeAuditUnavailable, false},
}

// DenialFor maps a business-level error to the rejection contract. ok is
// false for internal errors, which must not be reflected to clients. A
// statement whose failure rolled its transaction back says so in
// constraints.transaction.
func DenialFor(err error, decisionID string) (Denial, bool) {
	d, ok := denialFor(err, decisionID)
	var rolledBack *TransactionRolledBackError
	if ok && errors.As(err, &rolledBack) {
		constraints := map[string]any{"transaction": OutcomeRolledBack}
		maps.Copy(constraints, d.Constraints)
		d.Constraints = constraints
		d.Hints = append(d.Hints, "the transaction was rolled back: begin a new one and redo its writes")
	}
	var changed *schemaChanged
	if ok && errors.As(err, &changed) {
		d.Hints = append(d.Hints, "the table changed in the database: describe the entity again, "+
			"or ask an administrator to update its configuration")
	}
	return d, ok
}

func denialFor(err error, decisionID string) (Denial, bool) {
	if d, ok := typedDenial(err); ok {
		d.DecisionID = decisionID
		return d, true
	}
	for _, t := range transient {
		for _, e := range t.errs {
			if errors.Is(err, e) {
				return Denial{Code: t.code, Reason: e.Error(), Retryable: true, Hints: []string{t.hint},
					DecisionID: decisionID}, true
			}
		}
	}
	if errors.Is(err, ErrCallerRateLimited) {
		return Denial{
			Code: CodeRateLimited, Reason: err.Error(), Retryable: true,
			Hints:      []string{"retry after a moment, or make fewer calls at once"},
			DecisionID: decisionID,
		}, true
	}
	if errors.Is(err, budget.ErrExceeded) {
		return Denial{
			Code: CodeBudgetExceeded, Reason: err.Error(), Retryable: true,
			Hints: []string{
				"narrow the request (fewer rows, fields, or bytes) or retry after the session budget resets",
			},
			DecisionID: decisionID,
		}, true
	}
	for _, m := range sentinelDenials {
		if errors.Is(err, m.err) {
			reason := err.Error()
			if m.code == CodeUnauthorized {
				// Authorization denials are normalized for clients: a detailed
				// reason would let a restricted role enumerate hidden entities
				// and fields (TM-002). The full reason still reaches the audit
				// log through the error chain, correlated by decision ID.
				reason = ErrUnauthorized.Error()
			}
			return Denial{
				Code: m.code, Reason: reason, Retryable: m.retryable,
				DecisionID: decisionID,
			}, true
		}
	}
	return Denial{}, false
}

// typedDenial maps the error types that carry constraints for the agent.
func typedDenial(err error) (Denial, bool) {
	if d, ok := escapeDenial(err); ok {
		return d, true
	}
	if errors.Is(err, ErrOutcomeUnknown) {
		// Not retryable as is: a blind retry may apply the write twice.
		return Denial{
			Code: CodeOutcomeUnknown, Reason: ErrOutcomeUnknown.Error(), Retryable: false,
			Hints: []string{"read the rows the write affects (or, for a commit, what the transaction wrote) " +
				"before deciding to retry"},
		}, true
	}
	var ended *TransactionEndedError
	if errors.As(err, &ended) {
		return Denial{
			Code: CodeTransactionNotFound, Reason: ended.Error(), Retryable: false,
			Constraints: map[string]any{"outcome": ended.Outcome},
		}, true
	}
	var ce *cost.ExceededError
	if errors.As(err, &ce) {
		return costDenial(ce, ""), true
	}
	if d, ok := writeDenial(err); ok {
		return d, true
	}
	var ee *AmbiguousEntityError
	if errors.As(err, &ee) {
		return Denial{
			Code: CodeAmbiguousEntity, Reason: ee.Error(), Retryable: true,
			Constraints: map[string]any{"candidates": ee.Candidates},
			Hints:       []string{"retry with one of constraints.candidates as the entity"},
		}, true
	}
	var ae *AmbiguousFieldScopeError
	if errors.As(err, &ae) {
		return Denial{
			Code: CodeAmbiguousFieldScope, Reason: "requested fields span grants with different row scopes",
			Retryable:   true,
			Constraints: map[string]any{"fieldScopes": ae.FieldScopes},
			Hints:       []string{"retry with explicit fields contained in one of constraints.fieldScopes"},
		}, true
	}
	return Denial{}, false
}

// writeDenial maps a write refused for the values it writes.
func writeDenial(err error) (Denial, bool) {
	var cv *ConstraintViolationError
	if errors.As(err, &cv) {
		return Denial{
			Code: CodeConstraintViolation, Reason: cv.Error(), Retryable: true,
			Constraints: map[string]any{"kind": cv.Kind, "fields": cv.Fields},
			Hints:       []string{"change the values of constraints.fields (or check referenced rows) and retry"},
		}, true
	}
	var rp *RowPolicyViolationError
	if errors.As(err, &rp) {
		d := Denial{Code: CodeRowPolicyViolation, Reason: rp.Error(), Retryable: len(rp.Fields) > 0}
		if len(rp.Fields) > 0 {
			d.Constraints = map[string]any{"fields": rp.Fields}
			d.Hints = []string{"write values of constraints.fields that keep the row within your access"}
		}
		return d, true
	}
	return Denial{}, false
}

// costDenial exposes the cost gate's estimate and effective limits so the
// agent can tighten the request instead of retrying blindly.
func costDenial(ce *cost.ExceededError, decisionID string) Denial {
	constraints := map[string]any{
		"estimatedRows": ce.Plan.EstimatedRows,
		"scoreValue":    ce.Score.Value,
		"soft":          ce.Soft,
	}
	if scanned, known := ce.Plan.ScannedRowsKnown(); !known {
		constraints["scannedRowsUnknown"] = true
	} else if scanned > 0 {
		constraints["estimatedScannedRows"] = scanned
	}
	if ce.Threshold.MaxScannedRows > 0 {
		constraints["maxScannedRows"] = ce.Threshold.MaxScannedRows
	}
	if ce.Plan.EstimatedBytes > 0 {
		constraints["estimatedBytes"] = ce.Plan.EstimatedBytes
	}
	if ce.Threshold.MaxRows > 0 {
		constraints["maxRows"] = ce.Threshold.MaxRows
	}
	if ce.Threshold.MaxBytes > 0 {
		constraints["maxBytes"] = ce.Threshold.MaxBytes
	}
	return Denial{
		Code: CodeCostExceeded, Reason: ce.Error(), Retryable: true,
		Constraints: constraints, Hints: ce.Hints, DecisionID: decisionID,
	}
}

// denyUnauthorized attaches the authorizer's reason to ErrUnauthorized so the
// rejection contract can explain the denial instead of discarding it.
func denyUnauthorized(dec rbac.Decision) error {
	if len(dec.FieldScopes) > 0 {
		return &AmbiguousFieldScopeError{Reason: dec.Reason, FieldScopes: dec.FieldScopes}
	}
	if dec.Reason == "" {
		return ErrUnauthorized
	}
	return fmt.Errorf("%w: %s", ErrUnauthorized, dec.Reason)
}

// AmbiguousFieldScopeError reports that no single grant covers every field a
// request uses. FieldScopes are field sets the caller can already read, so
// exposing them is not a side channel. It unwraps to ErrUnauthorized.
type AmbiguousFieldScopeError struct {
	Reason      string
	FieldScopes [][]string
}

func (e *AmbiguousFieldScopeError) Error() string {
	return ErrUnauthorized.Error() + ": " + e.Reason
}

func (e *AmbiguousFieldScopeError) Unwrap() error { return ErrUnauthorized }

// NewDecisionID returns a 128-bit random identifier correlating one tool
// call's MCP response, audit event, and trace span. crypto/rand.Read never
// fails (since Go 1.24 it crashes the program instead), so the identifier is
// never predictable.
func NewDecisionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

type decisionIDCtxKey struct{}

// WithDecisionID attaches a decision ID to ctx for hooks and telemetry.
func WithDecisionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, decisionIDCtxKey{}, id)
}

// DecisionIDFromContext returns the decision ID attached by RunTool, or "".
func DecisionIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(decisionIDCtxKey{}).(string)
	return id
}

// ConstraintViolationError reports a write the database rejected for
// violating a constraint. Fields lists the violating columns the caller may
// see; the constraint name and the database message are never included.
type ConstraintViolationError struct {
	Kind   string
	Fields []string
}

func (e *ConstraintViolationError) Error() string {
	return ErrConstraintViolation.Error() + ": " + e.Kind
}

func (e *ConstraintViolationError) Unwrap() error { return ErrConstraintViolation }

// writeError classifies the database error of a write on res: a constraint
// violation becomes an actionable denial, anything else a database error.
func writeError(res entity.Resolved, err error) error {
	var ce *store.ConstraintError
	if !errors.As(err, &ce) {
		return WrapDBError(err)
	}
	columns := ce.Columns
	if len(columns) == 0 && ce.Constraint != "" {
		for _, k := range res.Entity.Keys {
			if k.Name == ce.Constraint {
				columns = k.Columns
			}
		}
	}
	fields := []string{}
	for _, c := range columns {
		if slices.ContainsFunc(res.Attributes, func(a entity.Attribute) bool { return a.Name == c }) {
			fields = append(fields, c)
		}
	}
	return &ConstraintViolationError{Kind: ce.Kind, Fields: fields}
}
