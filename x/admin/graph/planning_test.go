package graph

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/99designs/gqlgen/client"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// planningHarness is a harness with a configuration store for templates and
// tokens and the public schema scanned.
func planningHarness(t *testing.T) (*harness, *client.Client) {
	t.Helper()
	h := newHarness(t)
	spec := configstore.Spec{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "plans.db")}
	if err := configstore.Init(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	store, err := configstore.Open(context.Background(), spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h.resolver.App.Plans = store
	c := h.client(auth.PermAll)
	var listed map[string]any
	mustPost(t, c, `{ schemaList(datasource: "shop") { defaultSchema } }`, &listed)
	scanSchema(t, c, `datasource: "shop", schemas: ["public"]`)
	return h, c
}

type catalogResponse struct {
	Catalog struct {
		Total  int
		Counts struct{ All, New, Workspace, Published, Drift int }
		Tables []struct {
			Ref, Status  string
			Entity       *string
			Missing      []string
			HasKey       bool
			ReferencedBy int
			ForeignKeys  int
		}
		Coverage []struct {
			Datasource string
			Scanned    int
			Listed     *int
		}
	}
}

const catalogQuery = `query($f: CatalogFilter!) { catalog(input: {filter: $f}) { total
	counts { all new workspace published drift }
	tables { ref status entity missing hasKey referencedBy foreignKeys } coverage { datasource scanned listed } } }`

func TestCatalogAcrossStatuses(t *testing.T) {
	_, c := planningHarness(t)
	var all catalogResponse
	mustPost(t, c, catalogQuery, &all, client.Var("f", map[string]any{}))
	got := all.Catalog
	if got.Total != 2 || got.Counts.New != 1 || got.Counts.Published != 1 || got.Counts.Drift != 1 ||
		got.Coverage[0].Scanned != 1 || *got.Coverage[0].Listed != 1 {
		t.Fatalf("catalog = %+v", got)
	}
	customers, orders := got.Tables[0], got.Tables[1]
	if customers.Ref != "shop.public.customers" || *customers.Entity != "shop.customers" ||
		!slices.Equal(customers.Missing, []string{"legacy_col"}) || customers.ReferencedBy != 1 ||
		orders.Status != "NEW" || orders.ForeignKeys != 1 || !orders.HasKey {
		t.Fatalf("tables = %+v", got.Tables)
	}
	var fresh catalogResponse
	mustPost(t, c, catalogQuery, &fresh, client.Var("f", map[string]any{"status": []string{"NEW"}}))
	if fresh.Catalog.Total != 1 || fresh.Catalog.Counts.All != 2 {
		t.Fatalf("new only = %+v", fresh.Catalog)
	}
	var refs struct{ CatalogRefs []string }
	mustPost(t, c, `{ catalogRefs(filter: {search: "ORD"}) }`, &refs)
	if !slices.Equal(refs.CatalogRefs, []string{"shop.public.orders"}) {
		t.Fatalf("refs = %v", refs.CatalogRefs)
	}
	var detail struct {
		CatalogTable struct {
			Candidate struct{ Relationships []struct{ Target string } }
		}
	}
	mustPost(t, c, `{ catalogTable(ref: "shop.public.orders") { candidate { relationships { target } } } }`, &detail)
	if rel := detail.CatalogTable.Candidate.Relationships; len(rel) != 1 || rel[0].Target != "shop.customers" {
		t.Fatalf("detail = %+v", detail)
	}
	// A workspace that imports the table shows it as not yet published.
	var edited struct{ EditWorkspace workspaceState }
	mustPost(t, c, editMutation, &edited, client.Var("in", map[string]any{"items": []any{map[string]any{
		"kind": "entity", "key": "shop.public.orders", "value": map[string]any{
			"name": "orders", "datasource": "shop", "schema": "public", "fields": []any{map[string]any{"name": "id"}},
		},
	}}}))
	var drafted catalogResponse
	mustPost(t, c, catalogQuery, &drafted, client.Var("f", map[string]any{"status": []string{"WORKSPACE"}}))
	if drafted.Catalog.Total != 1 || drafted.Catalog.Tables[0].Ref != "shop.public.orders" {
		t.Fatalf("workspace = %+v", drafted.Catalog)
	}
}

const importPlan = `{"scope": {"status": ["new"]}, "import": true,
	"grants": [{"principal": "role:analyst", "actions": ["read"]}]}`

func TestPreviewPlanAndWorkspace(t *testing.T) {
	_, c := planningHarness(t)
	var preview struct {
		PreviewPlan struct {
			Added    []string
			Entities []struct{ Name string }
			Roles    []struct {
				Name   string
				Grants []struct{ Entity string }
			}
			Report struct {
				Matched int
				Changes []struct{ Kind string }
			}
			Validation struct {
				Ok    bool
				Error *string
			}
		}
	}
	mustPost(t, c, `query($p: JSON!) { previewPlan(input: {plan: $p}) {
		added entities { name } roles { name grants { entity } }
		report { matched changes { kind } } validation { ok error } } }`, &preview, client.Var("p", mustJSON(importPlan)))
	p := preview.PreviewPlan
	if !slices.Equal(p.Added, []string{"shop.public.orders"}) || len(p.Entities) != 1 || len(p.Roles) != 1 ||
		len(p.Roles[0].Grants) != 2 || p.Roles[0].Grants[0].Entity != "shop.customers" ||
		p.Roles[0].Grants[1].Entity != "shop.public.orders" || !p.Validation.Ok || p.Report.Matched != 1 {
		t.Fatalf("preview = %+v", p)
	}
	checkWorkspace(t, c)
	if err := c.Post(`{ previewPlan(input: {plan: {scope: {}}}) { added } }`, &preview); err == nil {
		t.Fatal("a plan with nothing to do must fail")
	}
}

const (
	readWorkspace = `{ workspace { base version sections conflicts { id path source mine theirs } } }`
	editMutation  = `mutation($in: WorkspaceEditInput!) { editWorkspace(input: $in) {
		version conflicts { id path source mine theirs } } }`
)

type workspaceState struct {
	Base      string
	Version   int
	Sections  map[string]any
	Conflicts []struct{ ID, Path, Source, Mine, Theirs string }
}

func (w workspaceState) role(name string) map[string]any {
	for _, r := range w.Sections["roles"].([]any) {
		if m := r.(map[string]any); m["name"] == name {
			return m
		}
	}
	return nil
}

func roleWrite(value, previous map[string]any) map[string]any {
	in := map[string]any{"kind": "role", "key": "support", "value": value}
	if previous != nil {
		in["previous"] = previous
	}
	return map[string]any{"items": []any{in}}
}

// checkWorkspace reads the signed-in administrator's workspace (the base's
// before anything is written) and writes items of it; two writes of one
// value made from the same read conflict, the later one kept, until the
// conflict is resolved; the workspace is then committed.
func checkWorkspace(t *testing.T, c *client.Client) {
	t.Helper()
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	if read.Workspace.Version != 0 || read.Workspace.Base != "1" || read.Workspace.Sections["entities"] == nil {
		t.Fatalf("a workspace before any was written = %+v", read)
	}
	var edited struct{ EditWorkspace workspaceState }
	support := map[string]any{"name": "support"}
	mustPost(t, c, editMutation, &edited, client.Var("in", roleWrite(support, nil)))
	a := map[string]any{"name": "support", "description": "a"}
	b := map[string]any{"name": "support", "description": "b"}
	mustPost(t, c, editMutation, &edited, client.Var("in", roleWrite(a, support)))
	mustPost(t, c, editMutation, &edited, client.Var("in", roleWrite(b, support)))
	if len(edited.EditWorkspace.Conflicts) != 1 || edited.EditWorkspace.Conflicts[0].Source != "workspace" ||
		edited.EditWorkspace.Conflicts[0].Path != "description" || edited.EditWorkspace.Conflicts[0].Mine != "b" {
		t.Fatalf("conflicts = %+v", edited.EditWorkspace.Conflicts)
	}
	commit := `mutation($v: Int!) { commitWorkspace(input: {version: $v, expectedPublished: "1"}) { id state } }`
	var committed struct{ CommitWorkspace struct{ ID, State string } }
	err := c.Post(commit, &committed, client.Var("v", edited.EditWorkspace.Version))
	if err == nil || !strings.Contains(err.Error(), `"code":"CONFLICTS"`) {
		t.Fatalf("committed with a conflict: %v", err)
	}
	var resolved struct{ ResolveConflict workspaceState }
	mustPost(t, c, `mutation($id: String!) { resolveConflict(id: $id, theirs: true) { version conflicts { id } } }`,
		&resolved, client.Var("id", edited.EditWorkspace.Conflicts[0].ID))
	mustPost(t, c, commit, &committed, client.Var("v", resolved.ResolveConflict.Version))
	mustPost(t, c, readWorkspace, &read)
	if committed.CommitWorkspace.State != "PUBLISHED" || read.Workspace.Base != committed.CommitWorkspace.ID ||
		read.Workspace.role("support")["description"] != "a" {
		t.Fatalf("committed = %+v, workspace = %+v", committed, read.Workspace)
	}
	var log struct{ WorkspaceLog []struct{ Op string } }
	mustPost(t, c, `{ workspaceLog { op } }`, &log)
	if len(log.WorkspaceLog) != 5 || log.WorkspaceLog[0].Op != "commit" {
		t.Fatalf("log = %+v", log)
	}
}

// The console's "add to the workspace" writes a previewed plan into the
// workspace on the server, as an agent's plan_apply does.
func TestApplyPlanFromTheConsole(t *testing.T) {
	_, c := planningHarness(t)
	var preview struct{ PreviewPlan struct{ PlanID string } }
	mustPost(t, c, `query($p: JSON!) { previewPlan(input: {plan: $p}) { planId } }`, &preview,
		client.Var("p", mustJSON(importPlan)))
	var applied struct {
		ApplyPlan struct{ Items []struct{ Kind, Key string } }
	}
	mustPost(t, c, `mutation($p: JSON!, $id: String!) { applyPlan(input: {plan: $p, planId: $id}) {
		items { kind key } } }`, &applied, client.Var("p", mustJSON(importPlan)),
		client.Var("id", preview.PreviewPlan.PlanID))
	var read struct{ Workspace workspaceState }
	mustPost(t, c, readWorkspace, &read)
	entities, _ := json.Marshal(read.Workspace.Sections["entities"])
	if len(applied.ApplyPlan.Items) == 0 || read.Workspace.Version != 1 ||
		!strings.Contains(string(entities), `"name":"orders"`) {
		t.Fatalf("applied = %+v, workspace = %+v", applied, read)
	}
	err := c.Post(`mutation($p: JSON!) { applyPlan(input: {plan: $p, planId: "stale"}) { items { key } } }`,
		&applied, client.Var("p", mustJSON(importPlan)))
	if err == nil || !strings.Contains(err.Error(), `"code":"CONFLICT"`) {
		t.Fatalf("a plan not previewed: %v", err)
	}
}

// Drafts are listed with what each builds on, up a chain of drafts (and
// discarded drafts).
func TestDraftsWithTheirBase(t *testing.T) {
	h, c := planningHarness(t)
	first := h.draftOn(t, "1", configedit.Edit{})
	second := h.draftOn(t, first, configedit.Edit{})
	var drafts struct {
		Drafts []struct {
			Revision struct{ ID string }
			BasedOn  string
		}
	}
	mustPost(t, c, `{ drafts { revision { id } basedOn } }`, &drafts)
	if len(drafts.Drafts) != 2 || drafts.Drafts[0].Revision.ID != second ||
		drafts.Drafts[0].BasedOn != "1" || drafts.Drafts[1].BasedOn != "1" {
		t.Fatalf("drafts = %+v", drafts)
	}
	// A discarded draft was never published: one building on it still builds
	// on what it built on.
	var discarded map[string]any
	mustPost(t, c, `mutation($id: ID!) { discardDraft(id: $id) { id } }`, &discarded,
		client.Var("id", first))
	mustPost(t, c, `{ drafts { revision { id } basedOn } }`, &drafts)
	if len(drafts.Drafts) != 1 || drafts.Drafts[0].BasedOn != "1" {
		t.Fatalf("drafts after discarding the first = %+v", drafts)
	}
}

const templateMutation = `mutation($in: PlanTemplateInput!) { savePlanTemplate(input: $in) { name version author } }`

type templateResponse struct {
	SavePlanTemplate struct {
		Name, Author string
		Version      int
	}
}

func checkTemplateVersions(t *testing.T, c *client.Client) {
	t.Helper()
	var saved templateResponse
	in := map[string]any{"name": "new tables", "plan": mustJSON(importPlan), "recurring": true}
	mustPost(t, c, templateMutation, &saved, client.Var("in", in))
	if err := c.Post(templateMutation, &saved, client.Var("in", in)); err == nil ||
		!strings.Contains(err.Error(), "exists") {
		t.Fatalf("creating a taken name: %v", err)
	}
	in["expectedVersion"] = 1
	mustPost(t, c, templateMutation, &saved, client.Var("in", in))
	if saved.SavePlanTemplate.Version != 2 {
		t.Fatalf("saved = %+v", saved)
	}
	if err := c.Post(templateMutation, &saved, client.Var("in", in)); err == nil ||
		!strings.Contains(err.Error(), "changed") {
		t.Fatalf("saving over a newer version: %v", err)
	}
	var versions struct{ PlanTemplateVersions []struct{ Version int } }
	mustPost(t, c, `{ planTemplateVersions(name: "new tables") { version } }`, &versions)
	if len(versions.PlanTemplateVersions) != 2 || versions.PlanTemplateVersions[0].Version != 2 {
		t.Fatalf("versions = %+v", versions)
	}
}

type pendingResponse struct {
	Pending struct {
		Plans []struct {
			Key      string
			Template struct{ Name string }
			Report   struct {
				Exceptions []struct{ Kind, Level string }
			}
			State *struct{ Status string }
		}
		DriftTables []struct {
			Key, Ref string
			Missing  []string
			State    *struct{ Status, Reason string }
		}
		Drift, NewTables, UnscannedSchemas int
		UnlistedDatasources                []string
	}
}

const pendingQuery = `{ pending { plans { key template { name } report { exceptions { kind level } } state { status } }
	driftTables { key ref missing state { status reason } } drift newTables unscannedSchemas unlistedDatasources } }`

// Reviewing a recurring template's proposal keeps the new tables a grant
// covers up for confirmation, as the pending page reported them.
func TestPreviewForReview(t *testing.T) {
	_, c := planningHarness(t)
	type response struct {
		PreviewPlan struct {
			Report struct {
				Exceptions []struct{ Kind, Level string }
			}
		}
	}
	const query = `query($p: JSON!, $r: Boolean) { previewPlan(input: {plan: $p, review: $r}) {
		report { exceptions { kind level } } } }`
	var plain, review response
	mustPost(t, c, query, &plain, client.Var("p", mustJSON(importPlan)), client.Var("r", false))
	mustPost(t, c, query, &review, client.Var("p", mustJSON(importPlan)), client.Var("r", true))
	if len(plain.PreviewPlan.Report.Exceptions) != 0 {
		t.Fatalf("plain = %+v", plain)
	}
	if x := review.PreviewPlan.Report.Exceptions; len(x) != 1 || x[0].Kind != "review" || x[0].Level != "confirm" {
		t.Fatalf("review = %+v", x)
	}
}

func TestTemplatesPendingAndTokens(t *testing.T) {
	_, c := planningHarness(t)
	checkTemplateVersions(t, c)
	var pending pendingResponse
	mustPost(t, c, pendingQuery, &pending)
	pw := pending.Pending
	if len(pw.Plans) != 1 || pw.Plans[0].Template.Name != "new tables" || pw.Drift != 1 || pw.NewTables != 1 ||
		pw.Plans[0].Report.Exceptions[0].Kind != "review" || pw.Plans[0].Report.Exceptions[0].Level != "confirm" ||
		len(
			pw.UnlistedDatasources,
		) != 0 || len(pw.DriftTables) != 1 || pw.DriftTables[0].Ref != "shop.public.customers" {
		t.Fatalf("pending = %+v", pw)
	}
	checkPendingStates(t, c, pw.DriftTables[0].Key)
	var deleted struct{ DeletePlanTemplate bool }
	mustPost(t, c, `mutation { deletePlanTemplate(name: "new tables") }`, &deleted)
	if err := c.Post(`mutation { deletePlanTemplate(name: "new tables") }`, &deleted); err == nil {
		t.Fatal("deleting an unknown template must fail")
	}
	checkMcpTokens(t, c)
}

// checkPendingStates ignores a pending item, which needs a reason, and
// reads the log.
func checkPendingStates(t *testing.T, c *client.Client, key string) {
	t.Helper()
	const set = `mutation($in: PendingStateInput!) { setPendingState(input: $in) { status } }`
	var resp map[string]any
	if err := c.Post(set, &resp, client.Var("in", map[string]any{"key": key, "status": "ignored"})); err == nil {
		t.Fatal("ignoring needs a reason")
	}
	mustPost(t, c, set, &resp, client.Var("in", map[string]any{"key": key, "status": "ignored", "reason": "历史字段"}))
	var pending pendingResponse
	mustPost(t, c, pendingQuery, &pending)
	if st := pending.Pending.DriftTables[0].State; st == nil || st.Status != "ignored" || st.Reason != "历史字段" {
		t.Fatalf("state = %+v", st)
	}
	var log struct {
		PendingLog []struct{ Status, UpdatedBy string }
	}
	mustPost(t, c, `query($k: String!) { pendingLog(key: $k) { status updatedBy } }`, &log, client.Var("k", key))
	if len(log.PendingLog) != 1 || log.PendingLog[0].UpdatedBy != "root" {
		t.Fatalf("log = %+v", log)
	}
}

// checkMcpTokens creates two named tokens, lists and revokes one.
func checkMcpTokens(t *testing.T, c *client.Client) {
	t.Helper()
	const create = `mutation($in: McpTokenInput!) { createMcpToken(input: $in) { token info { id name expiresAt } } }`
	var created struct {
		CreateMcpToken struct {
			Token string
			Info  struct {
				ID, Name  string
				ExpiresAt *string
			}
		}
	}
	mustPost(t, c, create, &created, client.Var("in", map[string]any{"name": "laptop", "expiresInDays": 30}))
	if created.CreateMcpToken.Token == "" || created.CreateMcpToken.Info.ExpiresAt == nil {
		t.Fatalf("created = %+v", created)
	}
	if err := c.Post(create, &created, client.Var("in", map[string]any{"name": "laptop"})); err == nil {
		t.Fatal("two tokens named alike")
	}
	mustPost(t, c, create, &created, client.Var("in", map[string]any{"name": "ci"}))
	var list struct{ McpTokens []struct{ ID, Name string } }
	mustPost(t, c, `{ mcpTokens { id name } }`, &list)
	if len(list.McpTokens) != 2 {
		t.Fatalf("tokens = %+v", list)
	}
	var revoked struct{ RevokeMcpToken bool }
	mustPost(t, c, `mutation($id: ID!) { revokeMcpToken(id: $id) }`, &revoked, client.Var("id", list.McpTokens[0].ID))
	var after struct{ McpTokens []struct{ Name string } }
	mustPost(t, c, `{ mcpTokens { name } }`, &after)
	if len(after.McpTokens) != 1 || after.McpTokens[0].Name != "ci" {
		t.Fatalf("after revoking = %+v", after)
	}
}

func TestCatalogLinksScopeAndBatches(t *testing.T) {
	_, c := planningHarness(t)
	var links struct {
		CatalogLinks struct {
			Outgoing, Incoming []struct {
				Ref    string
				Entity *string
			}
		}
	}
	mustPost(
		t,
		c,
		`{ catalogLinks(ref: "shop.public.customers") { outgoing { ref entity } incoming { ref entity } } }`,
		&links,
	)
	if l := links.CatalogLinks; len(l.Outgoing) != 0 || len(l.Incoming) != 1 ||
		l.Incoming[0].Ref != "shop.public.orders" ||
		l.Incoming[0].Entity != nil {
		t.Fatalf("links = %+v", l)
	}
	var stats struct {
		PlanScope struct {
			Total      int
			Samples    []struct{ Ref string }
			Conditions []struct {
				Name    string
				Matched int
			}
		}
	}
	mustPost(
		t,
		c,
		`query($s: JSON!) { planScope(scope: $s) { total samples { ref } conditions { name matched } } }`,
		&stats,
		client.Var("s", mustJSON(`{"tables": ["o*"], "status": ["published"]}`)),
	)
	if s := stats.PlanScope; s.Total != 0 || len(s.Conditions) != 2 || s.Conditions[0].Matched != 1 ||
		s.Conditions[1].Matched != 1 {
		t.Fatalf("scope = %+v", s)
	}
}

func TestScanBatches(t *testing.T) {
	_, c := planningHarness(t)
	var batch struct{ StartScanBatch struct{ ID string } }
	mustPost(t, c, `mutation { startScanBatch(input: {datasource: "shop", schemas: ["public"]}) { id } }`, &batch)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var all struct {
			ScanBatches []struct {
				ID, State string
				Done      int
			}
		}
		mustPost(t, c, `{ scanBatches(datasource: "shop") { id state done } }`, &all)
		if b := all.ScanBatches[0]; b.State == "done" && b.Done == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("batches = %+v", all)
		}
	}
	var retry map[string]any
	err := c.Post(
		`mutation($id: ID!) { retryScanBatch(id: $id) { id } }`,
		&retry,
		client.Var("id", batch.StartScanBatch.ID),
	)
	if err == nil || !strings.Contains(err.Error(), "nothing") {
		t.Fatalf("retrying a batch that did not fail: %v", err)
	}
}

func TestPlanningPermissions(t *testing.T) {
	h, _ := planningHarness(t)
	reader := h.client(auth.PermRead)
	for _, q := range []string{
		`{ catalog(input: {filter: {}}) { total } }`, `{ catalogRefs(filter: {}) }`, `{ pending { drift } }`,
		`{ previewPlan(input: {plan: {scope: {}, import: true}}) { added } }`,
		`{ catalogLinks(ref: "shop.public.orders") { incoming { ref } } }`,
		`mutation { saveWorkspace(input: {base: "1", sections: {}}) { version } }`,
		`mutation { savePlanTemplate(input: {name: "x", plan: {scope: {}, import: true}, recurring: false}) { name } }`,
		`mutation { startScanBatch(input: {datasource: "shop", schemas: ["public"]}) { id } }`,
		`mutation { setPendingState(input: {key: "k", status: "open"}) { status } }`,
	} {
		var resp map[string]any
		if err := reader.Post(q, &resp); err == nil {
			t.Errorf("a reader ran %s", q)
		}
	}
	var resp map[string]any
	for _, q := range []string{`{ planTemplates { name } }`, `mutation { createMcpToken(input: {name: "x"}) { token } }`} {
		if err := reader.Post(q, &resp); err != nil {
			t.Errorf("a reader cannot run %s: %v", q, err)
		}
	}
}
