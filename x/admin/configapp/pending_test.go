package configapp

import (
	"fmt"
	"testing"
)

// Pending lists at most pendingTables drift and gone tables, those not
// handled first, and counts the rest.
func TestFirstPendingKeepsUnhandledFirst(t *testing.T) {
	t.Parallel()
	var items []PendingGoneTable
	for i := range pendingTables + 50 {
		g := PendingGoneTable{Key: fmt.Sprintf("gone:%d", i)}
		switch {
		case i < 100:
			g.State = &PendingState{Status: "ignored"}
		case i < 120:
			g.State = &PendingState{Status: "open"}
		}
		items = append(items, g)
	}
	kept, more := firstPending(items)
	if len(kept) != pendingTables || more != 50 {
		t.Fatalf("kept %d, more %d", len(kept), more)
	}
	if kept[0].Key != "gone:100" || kept[149].Key != "gone:249" || kept[150].Key != "gone:0" {
		t.Fatalf("order: %s, %s, %s", kept[0].Key, kept[149].Key, kept[150].Key)
	}
	few := []PendingGoneTable{{Key: "gone:0", State: &PendingState{Status: "ignored"}}, {Key: "gone:1"}}
	if kept, more := firstPending(few); len(kept) != 2 || more != 0 || kept[0].Key != "gone:0" {
		t.Fatalf("a short list is kept as is: %d, %d", len(kept), more)
	}
}
