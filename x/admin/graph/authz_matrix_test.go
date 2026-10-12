package graph

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
)

// operation is one top-level field called with the least valid arguments.
type operation struct {
	kind, field, document string
}

// operations lists every query and mutation field of the schema, each with
// its required arguments filled in by type (an argument of a valid type, so
// the call reaches the resolver; whether the value makes sense does not
// matter) and __typename selected.
func operations() []operation {
	var out []operation
	for kind, def := range map[string]*ast.Definition{"query": parsedSchema.Query, "mutation": parsedSchema.Mutation} {
		for _, f := range def.Fields {
			if strings.HasPrefix(f.Name, "__") {
				continue
			}
			var args []string
			for _, a := range f.Arguments {
				if a.Type.NonNull {
					args = append(args, a.Name+": "+literal(a.Type, 0))
				}
			}
			call := f.Name
			if len(args) > 0 {
				call += "(" + strings.Join(args, ", ") + ")"
			}
			if selection(f.Type) {
				call += " { __typename }"
			}
			out = append(out, operation{kind: kind, field: f.Name, document: kind + " { " + call + " }"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].field < out[j].field })
	return out
}

func selection(t *ast.Type) bool {
	if t.Elem != nil {
		return selection(t.Elem)
	}
	def := parsedSchema.Types[t.NamedType]
	return def != nil && (def.Kind == ast.Object || def.Kind == ast.Interface || def.Kind == ast.Union)
}

// literal is a GraphQL value of type t.
func literal(t *ast.Type, depth int) string {
	if t.Elem != nil {
		return "[" + literal(t.Elem, depth) + "]"
	}
	def := parsedSchema.Types[t.NamedType]
	switch {
	case t.NamedType == "Int":
		return "1"
	case t.NamedType == "Float":
		return "1.5"
	case t.NamedType == "Boolean":
		return "false"
	case t.NamedType == "ID":
		return `"1"`
	case t.NamedType == "String":
		return `"x"`
	case def != nil && def.Kind == ast.Enum:
		return def.EnumValues[0].Name
	case def != nil && def.Kind == ast.InputObject && depth < 4:
		var fields []string
		for _, f := range def.Fields {
			if f.Type.NonNull {
				fields = append(fields, f.Name+": "+literal(f.Type, depth+1))
			}
		}
		return "{" + strings.Join(fields, ", ") + "}"
	}
	return "{scope: {}, import: true}" // a JSON scalar: a plan decodes from it, other values ignore it
}

var permissions = []string{auth.PermRead, auth.PermWrite, auth.PermPublish, auth.PermAccounts}

// subsets are every combination of the four permissions.
func subsets() [][]string {
	var out [][]string
	for mask := range 1 << len(permissions) {
		var set []string
		for i, p := range permissions {
			if mask&(1<<i) != 0 {
				set = append(set, p)
			}
		}
		out = append(out, set)
	}
	return out
}

// requiredPermissions is what each admin API operation requires: all of the
// listed permissions (none: any signed-in administrator). Every query and
// mutation of the schema must be listed: a new operation fails this test
// until its permissions are decided here.
var requiredPermissions = map[string][]string{
	// Any signed-in administrator.
	"me": {},
	// Reads.
	"capabilities":         {auth.PermRead},
	"configSchema":         {auth.PermRead},
	"createMcpToken":       {auth.PermRead},
	"diff":                 {auth.PermRead},
	"drafts":               {auth.PermRead},
	"fieldReferences":      {auth.PermRead},
	"mcpTokens":            {auth.PermRead},
	"pendingLog":           {auth.PermRead},
	"planTemplateVersions": {auth.PermRead},
	"planTemplates":        {auth.PermRead},
	"providerCapabilities": {auth.PermRead},
	"published":            {auth.PermRead},
	"revision":             {auth.PermRead},
	"revisions":            {auth.PermRead},
	"revokeMcpToken":       {auth.PermRead},
	"serverStatus":         {auth.PermRead},
	"simulate":             {auth.PermRead},
	"tableComments":        {auth.PermRead},
	"validate":             {auth.PermRead},
	"visibility":           {auth.PermRead},
	"workspace":            {auth.PermRead},
	"workspaceLog":         {auth.PermRead},
	// Edits of the caller's workspace, scans and plan templates.
	"cancelScanBatch":     {auth.PermWrite},
	"cancelSchemaScan":    {auth.PermWrite},
	"checkpointWorkspace": {auth.PermWrite},
	"deletePlanTemplate":  {auth.PermWrite},
	"discardDraft":        {auth.PermWrite},
	"editWorkspace":       {auth.PermWrite},
	"generateUserToken":   {auth.PermWrite},
	"mergeDraft":          {auth.PermWrite},
	"resetWorkspace":      {auth.PermWrite},
	"resolveConflict":     {auth.PermWrite},
	"retryScanBatch":      {auth.PermWrite},
	"savePlanTemplate":    {auth.PermWrite},
	"scanBatches":         {auth.PermWrite},
	"schemaList":          {auth.PermWrite},
	"schemaScan":          {auth.PermWrite},
	"schemaTables":        {auth.PermWrite},
	"setPendingState":     {auth.PermWrite},
	"startScanBatch":      {auth.PermWrite},
	"startSchemaScan":     {auth.PermWrite},
	"updateWorkspace":     {auth.PermWrite},
	// The catalog and plans (previewed or applied) read the live databases and the workspace.
	"applyPlan":    {auth.PermRead, auth.PermWrite},
	"catalog":      {auth.PermRead, auth.PermWrite},
	"catalogLinks": {auth.PermRead, auth.PermWrite},
	"catalogRefs":  {auth.PermRead, auth.PermWrite},
	"catalogTable": {auth.PermRead, auth.PermWrite},
	"pending":      {auth.PermRead, auth.PermWrite},
	"planScope":    {auth.PermRead, auth.PermWrite},
	"previewPlan":  {auth.PermRead, auth.PermWrite},
	// Publishing.
	"commitWorkspace": {auth.PermPublish},
	"publish":         {auth.PermPublish},
	"rollback":        {auth.PermPublish},
	// Administrator accounts (setAdminPassword: another account's password).
	"adminAccounts":      {auth.PermAccounts},
	"createAdminAccount": {auth.PermAccounts},
	"setAdminPassword":   {auth.PermAccounts},
	"updateAdminAccount": {auth.PermAccounts},
}

// TestEveryOperationEnforcesItsPermissions calls every operation of the
// admin API under every combination of permissions and checks it is refused
// exactly when the combination lacks one it requires.
func TestEveryOperationEnforcesItsPermissions(t *testing.T) {
	ops := operations()
	for _, op := range ops {
		if _, ok := requiredPermissions[op.field]; !ok {
			t.Errorf("%s %s has no entry in requiredPermissions", op.kind, op.field)
		}
	}
	allowed := map[string][][]string{}
	for _, held := range subsets() {
		h, _ := planningHarness(t)
		c := h.client(append([]string{}, held...)...)
		if len(held) == 0 {
			c = h.client("admin:none")
		}
		for _, op := range ops {
			var resp map[string]any
			err := c.Post(op.document, &resp)
			if err != nil && strings.Contains(err.Error(), "GRAPHQL_VALIDATION_FAILED") {
				t.Fatalf("%s does not validate: %v", op.document, err)
			}
			forbidden := err != nil && strings.Contains(err.Error(), auth.ErrForbidden.Error())
			if !forbidden {
				allowed[op.field] = append(allowed[op.field], held)
			}
			want, listed := requiredPermissions[op.field]
			if !listed {
				continue
			}
			if forbidden == containsAll(held, want) {
				t.Errorf("%s holding %v: forbidden = %v, requires %v (%v)", op.field, held, forbidden, want, err)
			}
		}
	}
	for _, op := range ops {
		if _, ok := requiredPermissions[op.field]; !ok {
			t.Logf("%s is allowed holding %v", op.field, allowed[op.field])
		}
	}
}

func containsAll(held, want []string) bool {
	for _, w := range want {
		if !slices.Contains(held, w) {
			return false
		}
	}
	return true
}

// A caller without the permission is refused before the input is read: a
// malformed plan or revision ID tells them nothing. Every operation that
// requires a permission is called with each ID and JSON argument malformed.
func TestAuthorizationPrecedesInput(t *testing.T) {
	h, _ := planningHarness(t)
	c := h.client("admin:none")
	for _, op := range operations() {
		if len(requiredPermissions[op.field]) == 0 {
			continue
		}
		document := strings.ReplaceAll(op.document, `"1"`, `"not an id"`)
		document = strings.ReplaceAll(document, "{scope: {}, import: true}", `"not a plan"`)
		var resp map[string]any
		err := c.Post(document, &resp)
		if err == nil || !strings.Contains(err.Error(), auth.ErrForbidden.Error()) {
			t.Errorf("%s: %v, want permission denied", document, err)
		}
	}
}
