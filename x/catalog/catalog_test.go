package catalog

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// What importing reads of an introspected table survives keeping it.
func TestTableRoundTrip(t *testing.T) {
	t.Parallel()
	e := entity.Entity{
		Name: "orders", Source: "orders", Schema: "sales", Description: "订单", Kind: entity.KindView, Derived: true,
		SideEffects: true,
		Attributes: []entity.Attribute{
			{Name: "id", Description: "主键", Domain: entity.Domain{Type: "bigint", AutoIncrement: true, Default: true}},
			{Name: "total", Domain: entity.Domain{Type: "numeric", Nullable: true, Generated: true}},
		},
		Keys: []entity.Key{
			{Name: "pk", Columns: []string{"id"}, Primary: true},
			{Name: "uq", Columns: []string{"total"}, Reason: entity.KeyNullable, Deferrable: true},
		},
		Indexes: []entity.Index{{Name: "ix", Method: "gin", Parts: []string{"lower(x)"}, Unique: true, Where: "x > 0"}},
		ForeignKeys: []entity.ForeignKey{{
			Name: "fk", Columns: []string{"id"}, RefSchema: "crm", RefRelation: "accounts", RefColumns: []string{"id"},
			OnDelete: entity.FKCascade,
		}},
	}
	data, err := json.Marshal(FromEntity(e))
	if err != nil {
		t.Fatal(err)
	}
	var kept Table
	if err := json.Unmarshal(data, &kept); err != nil {
		t.Fatal(err)
	}
	if got := kept.Entity(); !reflect.DeepEqual(got, e) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, e)
	}
}

func TestFromCatalogSplitsBySchema(t *testing.T) {
	t.Parallel()
	at := time.Unix(1, 0)
	cat := introspect.Catalog{Default: "a", Tables: []entity.Entity{
		{Name: "t1", Source: "t1", Schema: "a"}, {Name: "t2", Source: "t2", Schema: "b"},
	}}
	split := FromCatalog(cat, []string{"a", "b", "empty"}, at)
	if len(split) != 3 || len(split[0].Tables) != 1 || split[1].Tables[0].Name != "t2" || split[2].Tables == nil ||
		split[0].Default != "a" || !split[0].ScannedAt.Equal(at) {
		t.Fatalf("split = %+v", split)
	}
	if all := FromCatalog(cat, nil, at); len(all) != 1 || all[0].Name != "" || len(all[0].Tables) != 2 {
		t.Fatalf("default schema = %+v", all)
	}
	if got := Catalog(split); len(got.Tables) != 2 || got.Default != "a" {
		t.Fatalf("catalog = %+v", got)
	}
}

// A schema asked for in another case than the server names it is the
// server's when names fold (MySQL's lower_case_table_names), kept under the
// name asked; when they do not, it is another schema.
func TestFromCatalogFoldsSchemaCase(t *testing.T) {
	t.Parallel()
	cat := introspect.Catalog{FoldCase: true, Tables: []entity.Entity{{Name: "items", Source: "items", Schema: "test"}}}
	split := FromCatalog(cat, []string{"TEST"}, time.Unix(1, 0))
	if len(split) != 1 || split[0].Name != "TEST" || len(split[0].Tables) != 1 {
		t.Fatalf("folded = %+v", split)
	}
	cat.FoldCase = false
	if split := FromCatalog(cat, []string{"TEST"}, time.Unix(1, 0)); len(split[0].Tables) != 0 {
		t.Fatalf("case-sensitive = %+v", split)
	}
}
