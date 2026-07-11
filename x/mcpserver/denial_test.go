package mcpserver

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/tool"
)

func denialFromResult(t *testing.T, res *mcp.CallToolResult) tool.Denial {
	t.Helper()
	denial, ok := res.StructuredContent.(tool.Denial)
	if !ok {
		t.Fatalf("StructuredContent = %T, want tool.Denial", res.StructuredContent)
	}
	return denial
}

func TestToResultBusinessErrorCarriesDenial(t *testing.T) {
	err := fmt.Errorf("%w: role \"intern\" not permitted to read \"users\"", tool.ErrUnauthorized)
	res, protocolErr := toResult(err, "decision-1")
	if protocolErr != nil {
		t.Fatalf("business error escalated to protocol error: %v", protocolErr)
	}
	if !res.IsError {
		t.Fatal("business error must set IsError")
	}
	denial := denialFromResult(t, res)
	if denial.Code != tool.CodeUnauthorized || denial.DecisionID != "decision-1" {
		t.Fatalf("denial = %+v", denial)
	}
	if strings.Contains(denial.Reason, "users") || strings.Contains(denial.Reason, "intern") {
		t.Fatalf("client-visible reason must not echo entity/role detail: %q", denial.Reason)
	}
}

func TestToResultCostExceededIncludesHints(t *testing.T) {
	err := &cost.ExceededError{
		Plan:  cost.Plan{EstimatedRows: 50000},
		Score: cost.Score{Value: 20},
		Hints: []string{"add LIMIT or narrow the filter"},
	}
	res, protocolErr := toResult(err, "decision-2")
	if protocolErr != nil {
		t.Fatal(protocolErr)
	}
	denial := denialFromResult(t, res)
	if denial.Code != tool.CodeCostExceeded || !denial.Retryable {
		t.Fatalf("denial = %+v", denial)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "hints:") {
		t.Fatalf("text %q missing hints", text)
	}
}

func TestToResultBudgetExceededIsBusinessError(t *testing.T) {
	res, protocolErr := toResult(budget.ErrExceeded, "decision-3")
	if protocolErr != nil {
		t.Fatalf("budget rejection must be a business result, got protocol error %v", protocolErr)
	}
	denial := denialFromResult(t, res)
	if denial.Code != tool.CodeBudgetExceeded || !denial.Retryable {
		t.Fatalf("denial = %+v", denial)
	}
}

func TestToResultInternalErrorStaysOpaque(t *testing.T) {
	res, protocolErr := toResult(errors.New("pq: relation \"secret\" does not exist"), "decision-4")
	if res != nil {
		t.Fatal("internal errors must not produce a client-visible result")
	}
	if !errors.Is(protocolErr, errInternal) {
		t.Fatalf("protocol error = %v", protocolErr)
	}
}
