package tool

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

func recordToolAudit(
	ctx context.Context,
	tc Context,
	info Info,
	auditInput json.RawMessage,
	res Result,
	err error,
	start time.Time,
) {
	if tc.Auditor == nil {
		return
	}
	_ = tc.Auditor.Record(ctx, audit.Event{
		Time:         time.Now(),
		DecisionID:   tc.DecisionID,
		Role:         tc.Role,
		User:         tc.User,
		Roles:        tc.UserRoles,
		Grants:       res.Grants,
		Entity:       entityNameForTool(info.Name, auditInput, tc.Registry),
		Action:       info.Action,
		Tool:         info.Name,
		Input:        auditInput,
		Allowed:      err == nil,
		Code:         denialCode(err),
		Error:        errString(err),
		Duration:     time.Since(start),
		ReturnedRows: returnedRows(res),
	})
}

// denialCode maps a tool error to its stable machine code for the audit
// event, or "" for success and internal errors (which have no public code).
func denialCode(err error) string {
	if err == nil {
		return ""
	}
	if d, ok := DenialFor(err, ""); ok {
		return d.Code
	}
	return ""
}

// entityNameForTool derives the logical entity of a call from the input
// envelope, or for a procedure tool from its name, so an audit line can be
// interpreted without replaying the input.
func entityNameForTool(toolName string, input json.RawMessage, registry *entity.Registry) string {
	// A procedure tool's input holds its parameters, whatever their names.
	if strings.HasPrefix(toolName, "procedure_") {
		if registry == nil {
			return ""
		}
		t, _ := FindProcedureTool(registry, toolName)
		return t.Entity.Name
	}
	var envelope struct {
		Entity string `json:"entity"`
	}
	if decodeEnvelope(input, &envelope) == nil {
		return envelope.Entity
	}
	return ""
}
