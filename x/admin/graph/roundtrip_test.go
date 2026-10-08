package graph

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"testing"

	"github.com/99designs/gqlgen/client"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
	"github.com/nethinwei/sql-mcp-server/x/revisionops"
)

const roundTripFixture = "testdata/roundtrip.yaml"

// reservedFields are configuration fields the fixture cannot set because
// validation rejects any value in this version.
var reservedFields = map[string]bool{"RoleDefinition.Permissions": true, "UserConfig.Permissions": true}

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

const readConfig = `query($id: ID!) { revision(id: $id) { yaml config {
  entities { name source datasource schema kind description primaryKey params tenantPolicy legacyAccess
    mcp { dmlTools customTool trustedProcedure }
    fields { name alias description mask exclude }
    relationships { name target cardinality joinOn } }
  roles { name description grants { entity actions fieldsRestricted readFields writeFields rows } }
  users { name description roles subject disabled
    grants { entity actions fieldsRestricted readFields writeFields rows } }
  settings } } }`

// Reading a revision and saving it back unchanged, as the console does, must
// reproduce the payload: no field is lost or rewritten on the way through the
// read model, the draft input and configedit.
func TestDraftRoundTripKeepsEveryField(t *testing.T) {
	t.Setenv("SHOP_DSN", "postgres://localhost/shop")
	data, err := os.ReadFile(roundTripFixture)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := configyaml.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := revisionops.Normalize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	ctx := context.Background()
	base, err := h.store.Create(ctx, revision.Draft{Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	c := h.client(auth.PermAll)
	var read struct {
		Revision struct {
			YAML   string `json:"yaml"`
			Config struct {
				Entities []map[string]any `json:"entities"`
				Roles    []map[string]any `json:"roles"`
				Users    []map[string]any `json:"users"`
				Settings map[string]any   `json:"settings"`
			} `json:"config"`
		} `json:"revision"`
	}
	id := revisionID(base.ID)
	mustPost(t, c, readConfig, &read, client.Var("id", id))
	draft := map[string]any{
		"base": id, "entities": read.Revision.Config.Entities, "roles": read.Revision.Config.Roles,
		"users": read.Revision.Config.Users, "settings": read.Revision.Config.Settings,
	}
	var created struct{ CreateDraft struct{ ID string } }
	mustPost(t, c, `mutation($d: DraftInput!) { createDraft(draft: $d) { id } }`, &created, client.Var("d", draft))
	var again struct{ Revision struct{ YAML string } }
	mustPost(t, c, `query($id: ID!) { revision(id: $id) { yaml } }`, &again, client.Var("id", created.CreateDraft.ID))
	if again.Revision.YAML != read.Revision.YAML {
		diff, _ := revisionops.RawDiff("base", []byte(read.Revision.YAML), "saved", []byte(again.Revision.YAML))
		t.Fatalf("saving the read model back changed the configuration:\n%s", diff)
	}
}

func revisionID(id int64) string { return strconv.FormatInt(id, 10) }
