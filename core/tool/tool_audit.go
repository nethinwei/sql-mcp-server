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
	name string,
	auditInput json.RawMessage,
	res Result,
	err error,
	start time.Time,
) {
	if tc.Auditor == nil {
		return
	}
	entityName, action := auditEntityAction(name, auditInput, tc.Registry)
	_ = tc.Auditor.Record(ctx, audit.Event{
		Time:         time.Now(),
		DecisionID:   tc.DecisionID,
		Role:         tc.Role,
		Entity:       entityName,
		Action:       action,
		Tool:         name,
		Input:        auditInput,
		Allowed:      err == nil,
		Code:         denialCode(err),
		Error:        errString(err),
		Duration:     time.Since(start),
		ReturnedRows: returnedRowsForAudit(res),
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

// auditEntityAction derives the logical entity and action of a tool call so
// an audit line can be interpreted without replaying the input. The entity
// comes from the input envelope (or procedure-tool resolution); the action is
// determined by the tool name.
func auditEntityAction(toolName string, input json.RawMessage, registry *entity.Registry) (string, string) {
	return entityNameForTool(toolName, input, registry), actionForTool(toolName)
}

func actionForTool(toolName string) string {
	switch toolName {
	case "read_records":
		return entity.ActionRead.String()
	case "aggregate_records":
		return entity.ActionAggregate.String()
	case "create_record":
		return entity.ActionCreate.String()
	case "update_record":
		return entity.ActionUpdate.String()
	case "delete_record":
		return entity.ActionDelete.String()
	case "execute_entity":
		return entity.ActionExecute.String()
	case "describe_entities":
		return "describe"
	case "begin_transaction", "commit_transaction", "rollback_transaction":
		return "transaction"
	}
	if strings.HasPrefix(toolName, "procedure_") {
		return entity.ActionExecute.String()
	}
	return ""
}

func entityNameForTool(toolName string, input json.RawMessage, registry *entity.Registry) string {
	var envelope struct {
		Entity string `json:"entity"`
	}
	if decodeEnvelope(input, &envelope) == nil && envelope.Entity != "" {
		return envelope.Entity
	}
	if registry == nil || !strings.HasPrefix(toolName, "procedure_") {
		return ""
	}
	for _, candidate := range registry.Entities() {
		if candidate.Kind == entity.KindProcedure && ProcedureToolName(candidate.Name) == toolName {
			return candidate.Name
		}
	}
	return ""
}
