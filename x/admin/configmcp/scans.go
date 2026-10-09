package configmcp

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nethinwei/sql-mcp-server/x/admin/graph"
)

type scanIn struct {
	Datasource string   `json:"datasource"`
	Schemas    []string `json:"schemas,omitempty" jsonschema:"schemas to scan; empty: every listed schema not scanned yet"`
}

func registerScans(s *mcp.Server, r *graph.Resolver) {
	q, m := r.Query(), r.Mutation()
	add(s, tool("scan_schemas", "Scan schemas of a datasource on the server, a few at a time, as a batch: "+
		"the named ones, or every listed schema not scanned yet. Poll scan_status for progress.", false),
		func(ctx context.Context, in scanIn) (any, error) {
			schemas := in.Schemas
			if len(schemas) == 0 {
				list, err := q.SchemaList(ctx, in.Datasource, nil)
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
			return m.StartScanBatch(ctx, graph.ScanBatchInput{Datasource: in.Datasource, Schemas: schemas})
		})
	add(s, tool("scan_status", "Recent scan batches of a datasource, running ones first, with each schema's "+
		"state and error.", true),
		func(ctx context.Context, in datasourceIn) (any, error) { return q.ScanBatches(ctx, in.Datasource) })
}
