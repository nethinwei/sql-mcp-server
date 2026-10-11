package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/audit"
	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// recordToolAudit records a call's event: best-effort, except for a durably
// audited call (the SQL escape hatch, see escapeTrace), whose "end" event is
// written before returning and whose failure to write is returned.
func recordToolAudit(
	ctx context.Context,
	tc Context,
	info Info,
	auditInput json.RawMessage,
	res Result,
	err error,
	start time.Time,
) error {
	if tc.Auditor == nil {
		return nil
	}
	ev := audit.Event{
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
	}
	trace, durable := ctx.Value(escapeTraceKey{}).(*escapeTrace)
	if !durable {
		_ = tc.Auditor.Record(ctx, ev)
		return nil
	}
	trace.fill(&ev, "end")
	return recordDurably(ctx, tc, ev)
}

// recordDurably writes ev before returning, or reports the escape hatch's
// audit unavailable: with no durable sink, or a sink that failed.
func recordDurably(ctx context.Context, tc Context, ev audit.Event) error {
	d, ok := tc.Auditor.(audit.Durable)
	if !ok {
		return ErrAuditUnavailable
	}
	if err := d.RecordSync(ctx, ev); err != nil {
		return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
	}
	return nil
}

// auditSQLBegin records an execute_sql call before its statement runs, or
// refuses the call: the hatch is audited fail closed, so no record, no SQL.
func auditSQLBegin(ctx context.Context, tc Context, g grant, trace *escapeTrace) error {
	ev := audit.Event{
		Time: time.Now(), DecisionID: tc.DecisionID, Role: tc.Role, User: tc.User, Roles: tc.UserRoles,
		Grants: g.Grants, Action: ActionSQL, Tool: executeSQLName, Allowed: true,
	}
	trace.fill(&ev, "begin")
	return recordDurably(ctx, tc, ev)
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
