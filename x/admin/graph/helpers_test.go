package graph

import (
	"reflect"
	"testing"
)

// The status lists the entities the database cannot serve, by ID, and an
// empty list (not null) when there are none.
func TestServerStatusListsUnavailableEntities(t *testing.T) {
	st := serverStatus(func() RuntimeState {
		return RuntimeState{Unavailable: map[string]string{
			"ds.b": "its table is missing from the database", "ds.a": "columns missing from the database: x",
		}}
	})
	want := []UnavailableEntity{
		{Entity: "ds.a", Reason: "columns missing from the database: x"},
		{Entity: "ds.b", Reason: "its table is missing from the database", TableMissing: true},
	}
	if !reflect.DeepEqual(st.UnavailableEntities, want) {
		t.Fatalf("unavailable = %+v", st.UnavailableEntities)
	}
	none := serverStatus(func() RuntimeState { return RuntimeState{} })
	if none.UnavailableEntities == nil || len(none.UnavailableEntities) != 0 || none.UnreachableDatasources == nil {
		t.Fatalf("none unavailable = %#v, want empty lists", none)
	}
	down := serverStatus(func() RuntimeState { return RuntimeState{Unreachable: map[string]string{"legacy": "refused"}} })
	if want := []UnreachableDatasource{{Datasource: "legacy", Error: "refused"}}; !reflect.DeepEqual(
		down.UnreachableDatasources, want) {
		t.Fatalf("unreachable = %+v", down.UnreachableDatasources)
	}
}
