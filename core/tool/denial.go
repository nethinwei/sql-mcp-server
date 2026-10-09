package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
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
	CodeDatasourceForbidden = "DATASOURCE_FORBIDDEN"
)

var sentinelDenials = []struct {
	err       error
	code      string
	retryable bool
}{
	{ErrUnauthorized, CodeUnauthorized, false},
	{ErrEntityNotFound, CodeEntityNotFound, false},
	{ErrInvalidInput, CodeInvalidInput, true},
	{ErrDMLToolsDisabled, CodeDMLToolsDisabled, false},
	{ErrUnsafeWrite, CodeUnsafeWrite, true},
	{ErrDatasourceForbidden, CodeDatasourceForbidden, false},
	{ErrDatabase, CodeDatabaseError, false},
	{ErrTransactionNotFound, CodeTransactionNotFound, false},
	{ErrTransactionScope, CodeTransactionScope, false},
	{ErrTransactionCapacity, CodeTransactionCapacity, true},
	{ErrTransactionStale, CodeTransactionStale, false},
}

// DenialFor maps a business-level error to the rejection contract. ok is
// false for internal errors, which must not be reflected to clients.
func DenialFor(err error, decisionID string) (Denial, bool) {
	if d, ok := typedDenial(err); ok {
		d.DecisionID = decisionID
		return d, true
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
	var ce *cost.ExceededError
	if errors.As(err, &ce) {
		return costDenial(ce, ""), true
	}
	var cv *ConstraintViolationError
	if errors.As(err, &cv) {
		return Denial{
			Code: CodeConstraintViolation, Reason: cv.Error(), Retryable: true,
			Constraints: map[string]any{"kind": cv.Kind, "fields": cv.Fields},
			Hints:       []string{"change the values of constraints.fields (or check referenced rows) and retry"},
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

// costDenial exposes the cost gate's estimate and effective limits so the
// agent can tighten the request instead of retrying blindly.
func costDenial(ce *cost.ExceededError, decisionID string) Denial {
	constraints := map[string]any{
		"estimatedRows": ce.Plan.EstimatedRows,
		"scoreValue":    ce.Score.Value,
		"soft":          ce.Soft,
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
// call's MCP response, audit event, and trace span.
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
