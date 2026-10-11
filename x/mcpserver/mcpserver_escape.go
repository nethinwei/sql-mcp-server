package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
)

const executeSQLTool = "execute_sql"

// listEscapeHatchPerUser lists execute_sql to the callers who may use it on
// some datasource and hides it from the others: its switch registers it for
// every session. Listing is not the guard; the tool authorizes each call.
// A revoked permission hides the tool at the caller's next tools/list and
// refuses its calls at once.
func listEscapeHatchPerUser(acquire appAcquire) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			listed, ok := res.(*mcp.ListToolsResult)
			if err != nil || method != "tools/list" || !ok {
				return res, err
			}
			app, release, err := acquire()
			if err != nil {
				return nil, err
			}
			defer release()
			role, _ := callerIdentity(ctx, app)
			if mayExecuteSQL(app, role) {
				return listed, nil
			}
			kept := make([]*mcp.Tool, 0, len(listed.Tools))
			for _, t := range listed.Tools {
				if t.Name != executeSQLTool {
					kept = append(kept, t)
				}
			}
			listed.Tools = kept
			return listed, nil
		}
	}
}

// mayExecuteSQL reports whether role may use the escape hatch on some
// datasource admitting it (none does while tools.executeSql is off).
func mayExecuteSQL(app *bootstrap.App, role string) bool {
	checker, ok := app.Authorizer.(rbac.PermissionChecker)
	if !ok {
		return false
	}
	for datasource := range app.Escapes {
		if checker.HasPermission(role, config.SQLExecutePermission(datasource)) {
			return true
		}
	}
	return false
}
