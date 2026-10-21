package configmcp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/x/admin/configapp"
	"github.com/nethinwei/sql-mcp-server/x/catalog"
)

type scanIn struct {
	Datasource string   `json:"datasource"`
	Schemas    []string `json:"schemas,omitempty"    jsonschema:"schemas to scan; empty: every listed one not scanned yet"`
	RequestKey string   `json:"requestKey,omitempty" jsonschema:"names the request: sent again, it gets the same batch"`
}

func registerScans(s *mcp.Server, app Service) {
	add(s, tool("scan_schemas", "Scan schemas of a datasource on the server, a few at a time, as a batch: "+
		"the named ones, or every listed schema not scanned yet. Poll scan_status for progress.", false),
		func(ctx context.Context, in scanIn) (any, error) {
			schemas := in.Schemas
			if len(schemas) == 0 {
				list, err := app.SchemaList(ctx, in.Datasource, false)
				if err != nil {
					return nil, err
				}
				for _, sc := range list.Schemas {
					if sc.ScannedAt == nil {
						schemas = append(schemas, sc.Name)
					}
				}
				if len(schemas) == 0 {
					return nil, errors.New("every listed schema is scanned; name the schemas to scan again")
				}
			}
			return app.StartScanBatch(ctx, configapp.ScanBatchInput{Datasource: in.Datasource, Schemas: schemas,
				RequestKey: &in.RequestKey})
		})
	add(s, tool("scan_status", fmt.Sprintf("Recent scan batches of a datasource, running ones first, with "+
		"the state and error of up to %d schemas each, those not done first (moreItems counts the rest).",
		scanStatusItems), true),
		func(ctx context.Context, in datasourceIn) (any, error) {
			all, err := app.ScanBatchesWithItems(ctx, in.Datasource)
			if err != nil {
				return nil, err
			}
			out := make([]scanStatus, 0, len(all))
			for _, b := range all {
				out = append(out, statusOf(b))
			}
			return out, nil
		})
}

// scanStatusItems bounds the items scan_status shows of a batch: a batch
// may have thousands of schemas; its counts cover them all.
const scanStatusItems = 100

// scanStatus is a batch as scan_status shows it.
type scanStatus struct {
	configapp.ScanBatch
	MoreItems int `json:"moreItems,omitempty"`
}

// statusOf keeps the first scanStatusItems items of b, those not done
// first.
func statusOf(b configapp.ScanBatch) scanStatus {
	items := slices.Clone(b.Items)
	slices.SortStableFunc(items, func(x, y configapp.ScanBatchItem) int {
		return boolRank(x.State == catalog.ItemDone) - boolRank(y.State == catalog.ItemDone)
	})
	out := scanStatus{ScanBatch: b}
	if len(items) > scanStatusItems {
		out.MoreItems, items = len(items)-scanStatusItems, items[:scanStatusItems]
	}
	out.Items = items
	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}
