package graph

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/99designs/gqlgen/graphql"

	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/workspaceops"
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

// A commit published whose workspace was not moved answers the revision,
// with the error beside it.
func TestCommittedNotMoved(t *testing.T) {
	t.Parallel()
	ctx := graphql.WithResponseContext(context.Background(), graphql.DefaultErrorPresenter, nil)
	rev, err := committed(ctx, revision.Revision{ID: 5},
		&workspaceops.NotMovedError{Published: revision.Revision{ID: 5}, Err: errors.New("store down")})
	if err != nil || rev == nil || rev.ID != "5" {
		t.Fatalf("committed = %+v, %v", rev, err)
	}
	errs := graphql.GetErrors(ctx)
	if len(errs) != 1 || errs[0].Extensions["code"] != "NOT_MOVED" || errs[0].Extensions["published"] != "5" {
		t.Fatalf("errors = %+v", errs)
	}
}
