package configmcp

import (
	"fmt"
	"testing"

	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

// scan_status shows at most scanStatusItems schemas of a batch, those not
// done first, and counts the rest.
func TestScanStatusBoundsTheItems(t *testing.T) {
	t.Parallel()
	var b configapp.ScanBatch
	for i := range scanStatusItems + 50 {
		state := catalog.ItemDone
		if i%10 == 9 {
			state = catalog.ItemFailed
		}
		b.Items = append(b.Items, configapp.ScanBatchItem{Schema: fmt.Sprint("s", i), State: state})
	}
	got := statusOf(b)
	if len(got.Items) != scanStatusItems || got.MoreItems != 50 || len(b.Items) != scanStatusItems+50 {
		t.Fatalf("items = %d, more = %d", len(got.Items), got.MoreItems)
	}
	for i, it := range got.Items {
		if (i < 15) != (it.State == catalog.ItemFailed) {
			t.Fatalf("item %d = %+v: the failed ones come first", i, it)
		}
	}
	if got.Items[0].Schema != "s9" {
		t.Fatalf("first = %+v", got.Items[0])
	}
}
