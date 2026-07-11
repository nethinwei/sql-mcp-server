package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

// TestDenialForSentinels pins the stable machine code and retryable flag for
// every business sentinel. Changing a code or flag is a breaking contract
// change and must be reflected in docs/tool-contract.md and the CHANGELOG.
func TestDenialForSentinels(t *testing.T) {
	cases := []struct {
		err       error
		code      string
		retryable bool
	}{
		{ErrUnauthorized, "UNAUTHORIZED", false},
		{ErrEntityNotFound, "ENTITY_NOT_FOUND", false},
		{ErrInvalidInput, "INVALID_INPUT", true},
		{ErrDMLToolsDisabled, "DML_TOOLS_DISABLED", false},
		{ErrUnsafeWrite, "UNSAFE_WRITE", true},
		{ErrNotImplemented, "NOT_IMPLEMENTED", false},
		{ErrDatabase, "DATABASE_ERROR", false},
		{ErrTransactionNotFound, "TRANSACTION_NOT_FOUND", false},
		{ErrTransactionScope, "TRANSACTION_SCOPE", false},
		{ErrTransactionCapacity, "TRANSACTION_CAPACITY", true},
	}
	for _, c := range cases {
		denial, ok := DenialFor(c.err, "d1")
		if !ok {
			t.Fatalf("DenialFor(%v) not mapped", c.err)
		}
		if denial.Code != c.code || denial.Retryable != c.retryable {
			t.Fatalf("DenialFor(%v) = %q retryable=%v, want %q retryable=%v",
				c.err, denial.Code, denial.Retryable, c.code, c.retryable)
		}
		if denial.DecisionID != "d1" {
			t.Fatalf("DenialFor(%v) decision ID = %q", c.err, denial.DecisionID)
		}
	}
}

func TestDenialForWrappedSentinelKeepsReason(t *testing.T) {
	err := fmt.Errorf("%w: role \"intern\" not permitted to read \"users\"", ErrUnauthorized)
	denial, ok := DenialFor(err, "d2")
	if !ok || denial.Code != CodeUnauthorized {
		t.Fatalf("wrapped sentinel not mapped: %+v ok=%v", denial, ok)
	}
	if denial.Reason != err.Error() {
		t.Fatalf("reason %q does not carry wrapped detail", denial.Reason)
	}
}

func TestDenialForCostExceeded(t *testing.T) {
	err := &cost.ExceededError{
		Plan:      cost.Plan{EstimatedRows: 50000, EstimatedBytes: 1 << 20},
		Score:     cost.Score{Value: 20},
		Threshold: cost.Threshold{MaxRows: 1000, MaxBytes: 4096},
		Hints:     []string{"add LIMIT or narrow the filter"},
	}
	denial, ok := DenialFor(err, "d3")
	if !ok || denial.Code != CodeCostExceeded || !denial.Retryable {
		t.Fatalf("cost denial = %+v ok=%v", denial, ok)
	}
	if denial.Constraints["estimatedRows"] != int64(50000) ||
		denial.Constraints["maxRows"] != int64(1000) ||
		denial.Constraints["maxBytes"] != int64(4096) {
		t.Fatalf("constraints missing limits: %v", denial.Constraints)
	}
	if len(denial.Hints) != 1 {
		t.Fatalf("hints not propagated: %v", denial.Hints)
	}
}

func TestDenialForBudgetExceeded(t *testing.T) {
	err := fmt.Errorf("%w: estimated scanned rows 9000 exceed limit 100", budget.ErrExceeded)
	denial, ok := DenialFor(err, "d4")
	if !ok || denial.Code != CodeBudgetExceeded || !denial.Retryable {
		t.Fatalf("budget denial = %+v ok=%v", denial, ok)
	}
	if len(denial.Hints) == 0 {
		t.Fatal("budget denial should carry a narrowing hint")
	}
}

func TestDenialForInternalErrorNotMapped(t *testing.T) {
	if _, ok := DenialFor(errors.New("driver: connection refused"), "d5"); ok {
		t.Fatal("internal error must not map to a client-visible denial")
	}
}

// TestDenialJSONGolden pins the wire format. The exact field names are the
// machine-readable contract; a mismatch here is a breaking change.
func TestDenialJSONGolden(t *testing.T) {
	denial := Denial{
		Code:        CodeCostExceeded,
		Reason:      "cost gate hard reject: score=20 rows=50000 cost=0",
		Retryable:   true,
		Constraints: map[string]any{"estimatedRows": int64(50000)},
		Hints:       []string{"add LIMIT or narrow the filter"},
		DecisionID:  "0123456789abcdef0123456789abcdef",
	}
	got, err := json.Marshal(denial)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"code":"COST_EXCEEDED",` +
		`"reason":"cost gate hard reject: score=20 rows=50000 cost=0",` +
		`"retryable":true,` +
		`"constraints":{"estimatedRows":50000},` +
		`"hints":["add LIMIT or narrow the filter"],` +
		`"decisionId":"0123456789abcdef0123456789abcdef"}`
	if string(got) != want {
		t.Fatalf("denial JSON drifted:\n got %s\nwant %s", got, want)
	}
}

func TestDenyUnauthorizedAttachesReason(t *testing.T) {
	err := denyUnauthorized(rbac.Decision{Reason: "role \"intern\" not permitted"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatal("denyUnauthorized must keep the sentinel")
	}
	if err.Error() == ErrUnauthorized.Error() {
		t.Fatal("denyUnauthorized must carry the reason")
	}
	if denyUnauthorized(rbac.Decision{}) != ErrUnauthorized {
		t.Fatal("empty reason must return the bare sentinel")
	}
}

func TestNewDecisionID(t *testing.T) {
	a, b := NewDecisionID(), NewDecisionID()
	if len(a) != 32 || len(b) != 32 {
		t.Fatalf("decision IDs must be 32 hex chars, got %q %q", a, b)
	}
	if a == b {
		t.Fatal("decision IDs must be unique")
	}
}

func TestDecisionIDContextRoundtrip(t *testing.T) {
	ctx := WithDecisionID(context.Background(), "abc")
	if DecisionIDFromContext(ctx) != "abc" {
		t.Fatal("decision ID not round-tripped through context")
	}
	if DecisionIDFromContext(context.Background()) != "" {
		t.Fatal("missing decision ID must yield empty string")
	}
}
