package graph

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/99designs/gqlgen/client"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

const roundTripFixture = "testdata/roundtrip.yaml"

// reservedFields are configuration fields the fixture cannot set because
// validation rejects any value in this version.
var reservedFields = map[string]bool{}

// Every editable configuration field is set somewhere in the fixture, so the
// round trip below exercises it.
func TestRoundTripFixtureCoversEveryField(t *testing.T) {
	data, err := os.ReadFile(roundTripFixture)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configyaml.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	seen := setFields(cfg.Entities, cfg.Roles, cfg.Users)
	for _, typ := range []reflect.Type{
		reflect.TypeFor[config.EntityConfig](), reflect.TypeFor[config.FieldConfig](),
		reflect.TypeFor[config.RelationshipConfig](), reflect.TypeFor[config.RoleConfig](),
		reflect.TypeFor[config.FieldACLConfig](), reflect.TypeFor[config.MCPFlags](),
		reflect.TypeFor[config.RoleDefinition](), reflect.TypeFor[config.GrantConfig](),
		reflect.TypeFor[config.UserConfig](),
	} {
		for i := range typ.NumField() {
			f := typ.Field(i)
			key := typ.Name() + "." + f.Name
			if config.YAMLName(f) != "" && !seen[key] && !reservedFields[key] {
				t.Errorf("%s is not set in %s", key, roundTripFixture)
			}
		}
	}
}

// setFields returns "Type.Field" for every configuration field set to a
// non-zero value anywhere in values.
func setFields(values ...any) map[string]bool {
	seen := map[string]bool{}
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				walk(v.MapIndex(k))
			}
		case reflect.Struct:
			for i := range v.NumField() {
				f := v.Type().Field(i)
				if config.YAMLName(f) == "" {
					continue
				}
				if !v.Field(i).IsZero() {
					seen[v.Type().Name()+"."+f.Name] = true
				}
				walk(v.Field(i))
			}
		}
	}
	for _, v := range values {
		walk(reflect.ValueOf(v))
	}
	return seen
}

// Reading a revision's sections and writing every item back into the
// workspace, as the console does, then saving the workspace, must reproduce
// the payload: no field is lost or rewritten on the way through the items,
// the admin API's JSON and configedit.
func TestWorkspaceRoundTripKeepsEveryField(t *testing.T) {
	payload := roundTripPayload(t)
	h, c := planningHarness(t)
	ctx := context.Background()
	rev, err := h.store.Create(ctx, revision.Draft{ParentID: 1, Payload: payload})
	if err == nil {
		_, err = h.store.Publish(ctx, rev.ID, 1, revision.Meta{})
	}
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Revision struct{ Sections configapp.Sections }
	}
	mustPost(t, c, `query($id: ID!) { revision(id: $id) { sections } }`, &read, client.Var("id", revisionID(rev.ID)))
	sections := read.Revision.Sections
	var items []map[string]any
	write := func(kind, key string, value any) {
		items = append(items, map[string]any{"kind": kind, "key": key, "value": value, "previous": value})
	}
	for _, e := range sections.Entities {
		write("entity", entity.ID(kit.Deref(e.Datasource), kit.Deref(e.Schema), e.Name), e)
	}
	for _, r := range sections.Roles {
		write("role", r.Name, r)
	}
	for _, u := range sections.Users {
		write("user", u.Name, u)
	}
	write("settings", "settings", sections.Settings)
	if saved := h.checkpoint(t, c, items...); saved != string(payload) {
		diff, _ := revisionops.RawDiff("published", payload, "saved", []byte(saved))
		t.Fatalf("writing the sections back changed the configuration:\n%s", diff)
	}
}

func revisionID(id int64) string { return strconv.FormatInt(id, 10) }

// A revision's sections are the configuration in the shape a workspace holds
// it (configapp's fixture), which the console takes as its baseline.
func TestRevisionSectionsAreTheWorkspaceShape(t *testing.T) {
	t.Setenv("SHOP_DSN", "postgres://localhost/shop")
	payload := roundTripPayload(t)
	h := newHarness(t)
	base, err := h.store.Create(context.Background(), revision.Draft{Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	var read struct{ Revision struct{ Sections any } }
	mustPost(t, h.client(auth.PermAll), `query($id: ID!) { revision(id: $id) { sections } }`, &read,
		client.Var("id", revisionID(base.ID)))
	fixture, err := os.ReadFile("../configapp/testdata/workspace_sections.json")
	if err != nil {
		t.Fatal(err)
	}
	var want any
	if err := json.Unmarshal(fixture, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read.Revision.Sections, want) {
		t.Fatalf("revision sections differ from the workspace shape:\n%v", read.Revision.Sections)
	}
}

// roundTripPayload is the round trip configuration as stored: what it says.
func roundTripPayload(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(roundTripFixture)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := configyaml.DecodeSpec(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := revisionops.Normalize(spec)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
