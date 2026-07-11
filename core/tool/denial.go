package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
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
	CodeNotImplemented      = "NOT_IMPLEMENTED"
	CodeDatabaseError       = "DATABASE_ERROR"
	CodeCostExceeded        = "COST_EXCEEDED"
	CodeBudgetExceeded      = "BUDGET_EXCEEDED"
	CodeTransactionNotFound = "TRANSACTION_NOT_FOUND"
	CodeTransactionScope    = "TRANSACTION_SCOPE"
	CodeTransactionCapacity = "TRANSACTION_CAPACITY"
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
	{ErrNotImplemented, CodeNotImplemented, false},
	{ErrDatabase, CodeDatabaseError, false},
	{ErrTransactionNotFound, CodeTransactionNotFound, false},
	{ErrTransactionScope, CodeTransactionScope, false},
	{ErrTransactionCapacity, CodeTransactionCapacity, true},
}

// DenialFor maps a business-level error to the rejection contract. ok is
// false for internal errors, which must not be reflected to clients.
func DenialFor(err error, decisionID string) (Denial, bool) {
	var ce *cost.ExceededError
	if errors.As(err, &ce) {
		return costDenial(ce, decisionID), true
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
	if dec.Reason == "" {
		return ErrUnauthorized
	}
	return fmt.Errorf("%w: %s", ErrUnauthorized, dec.Reason)
}

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
