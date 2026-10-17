package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/transport"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// The console's workspace selection (web/admin/src/api/ops.ts WorkspaceParts):
// the items changed since the version it holds ($since), all when null.
const benchWorkspaceParts = `base version items(since: $since) { complete items { kind key value } } updatedBy taken
	checkpoint conflicts { id kind key path mine mineGone theirs theirsGone source fromId fromBy }
	notes { source by text }`

// largeYAML is a configuration at the scale of a large deployment: entities
// of fields each over one datasource, roles granting 60 entities each, and
// users holding two roles each.
func largeYAML(entities, fields, roles, users int) string {
	var b strings.Builder
	b.WriteString("databases:\n  shop:\n    driver: postgres\n    dsn: ${SHOP_DSN}\nentities:\n")
	for i := range entities {
		fmt.Fprintf(&b, "  - name: t%04d\n    datasource: shop\n    schema: s%d\n    primaryKey: [c0]\n"+
			"    description: table %d\n    fields:\n", i, i%10, i)
		for f := range fields {
			fmt.Fprintf(&b, "      - {name: c%d, description: column %d}\n", f, f)
		}
	}
	b.WriteString("roles:\n")
	for r := range roles {
		fmt.Fprintf(&b, "  r%d:\n    grants:\n", r)
		for g := range 60 {
			e := (r*60 + g) % entities
			fmt.Fprintf(&b, "      - {entity: s%d.t%04d, actions: [read]}\n", e%10, e)
		}
	}
	b.WriteString("users:\n")
	for u := range users {
		fmt.Fprintf(&b, "  u%d:\n    roles: [r%d, r%d]\n", u, u%roles, (u+1)%roles)
	}
	return b.String()
}

// largeWorkspace serves the admin API over a published large configuration,
// with workspaces kept in a SQLite store, as root.
func largeWorkspace(b *testing.B) http.Handler {
	b.Helper()
	b.Setenv("SHOP_DSN", "postgres://localhost/shop")
	ctx := context.Background()
	store := revision.NewMemoryStore(nil)
	d, err := store.Create(ctx, revision.Draft{Payload: []byte(largeYAML(3000, 20, 50, 200))})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := store.Publish(ctx, d.ID, 0, revision.Meta{}); err != nil {
		b.Fatal(err)
	}
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(b.TempDir(), "plans.db")}
	if err := configstore.Init(ctx, spec, nil); err != nil {
		b.Fatal(err)
	}
	plans, err := configstore.Open(ctx, spec, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = plans.Close() })
	srv := handler.New(NewExecutableSchema(Config{Resolvers: &Resolver{
		App: &configapp.Service{Store: store, Plans: plans},
	}}))
	srv.AddTransport(transport.POST{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(),
			auth.Principal{Username: "root", Permissions: []string{auth.PermAll}})))
	})
}

// post runs query and returns the response body.
func post(b *testing.B, h http.Handler, query string, vars map[string]any) []byte {
	b.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if bytes.Contains(rec.Body.Bytes(), []byte(`"errors"`)) {
		b.Fatalf("%s", rec.Body.Bytes()[:min(rec.Body.Len(), 500)])
	}
	return rec.Body.Bytes()
}

// BenchmarkWorkspaceRead reads the workspace as the console does on load and
// on every server signal.
func BenchmarkWorkspaceRead(b *testing.B) {
	h := largeWorkspace(b)
	b.ReportAllocs()
	var n int
	for b.Loop() {
		n = len(post(b, h, `query($since: Int) { workspace { `+benchWorkspaceParts+` } }`, nil))
	}
	b.ReportMetric(float64(n), "resp-bytes")
}

// BenchmarkWorkspaceEdit writes one role of the workspace as the console
// does after an edit, with the answer it selects.
func BenchmarkWorkspaceEdit(b *testing.B) {
	h := largeWorkspace(b)
	edit := `mutation($in: WorkspaceEditInput!, $since: Int) { editWorkspace(input: $in) { ` +
		benchWorkspaceParts + ` } }`
	role := func(i int) map[string]any {
		return map[string]any{"name": "r0", "grants": []any{
			map[string]any{"entity": fmt.Sprintf("s%d.t%04d", i%10, i%3000), "actions": []any{"READ"}},
		}}
	}
	var previous any
	var n, i int
	b.ReportAllocs()
	for b.Loop() {
		in := map[string]any{"kind": "role", "key": "r0", "value": role(i), "previous": previous}
		answer := post(b, h, edit, map[string]any{"in": map[string]any{"items": []any{in}}, "since": i})
		n = len(answer)
		if !bytes.Contains(answer, []byte(`"complete":false`)) {
			b.Fatalf("answered every item: %.200s", answer)
		}
		previous = role(i)
		i++
	}
	b.ReportMetric(float64(n), "resp-bytes")
}
