package graph

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
)

// schemaList lists the schemas of datasource with what was scanned of each;
// the list is read from the database once, and again on refresh.
func (r *Resolver) schemaList(ctx context.Context, datasource string, refresh bool) (*SchemaList, error) {
	cfg, scans, src, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	listed, err := scans.list(src, refresh, func() (listedSchemas, error) {
		return r.listSchemas(ctx, datasource, cfg.Databases[datasource])
	})
	if err != nil {
		return nil, err
	}
	out := &SchemaList{
		Datasource: datasource, Schemas: make([]SchemaInfo, 0, len(listed.all)),
		DefaultSchema: optional(listed.current), ListedAt: listed.at, Source: src.token(),
	}
	names := listed.all
	if len(names) == 0 {
		names = []string{""} // the default schema of a datasource that cannot list them
	}
	for _, name := range names {
		info := SchemaInfo{Name: name}
		if found, ok := scans.scanned(src, name); ok {
			info.ScannedAt, info.Tables = &found.at, new(len(found.cat.Tables))
		}
		out.Schemas = append(out.Schemas, info)
	}
	return out, nil
}

func (r *Resolver) listSchemas(
	ctx context.Context, datasource string, database config.DatabaseConfig,
) (listedSchemas, error) {
	ctx, cancel := context.WithTimeout(ctx, requestScanTimeout)
	defer cancel()
	var listed listedSchemas
	err := r.Introspect(ctx, datasource, database, func(in introspect.Introspector) (err error) {
		if lister, ok := in.(introspect.SchemaLister); ok {
			listed.all, listed.current, err = lister.Schemas(ctx)
		}
		return err
	})
	if err != nil {
		return listed, fmt.Errorf("introspect %q: %w", datasource, err)
	}
	return listed, nil
}

// schemaTables returns the tables of the kept ones of schemas (nil: all).
func (r *Resolver) schemaTables(ctx context.Context, datasource string, schemas []string) (*SchemaImport, error) {
	cfg, scans, src, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	return scans.importOf(src, schemas, cfg), nil
}

// startSchemaScan queues a scan of schemas of datasource (none: every schema
// it lists, or its default one) and keeps what it finds.
func (r *Resolver) startSchemaScan(ctx context.Context, datasource string, schemas []string) (*ScanJob, error) {
	cfg, scans, src, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	job, err := scans.pool.Start(datasource, func(ctx context.Context) (struct{}, error) {
		var cat introspect.Catalog
		var scanned []string
		// The scan reads the database of the configuration it was started
		// with, which src names, even when it is reloaded meanwhile.
		err := r.Introspect(ctx, datasource, cfg.Databases[datasource], func(in introspect.Introspector) (err error) {
			cat, scanned, err = importCatalog(ctx, in, schemas)
			return err
		})
		if err != nil {
			return struct{}{}, fmt.Errorf("introspect %q: %w", datasource, err)
		}
		if len(scanned) == 0 {
			scanned = []string{""}
		}
		scans.keep(src, scanned, cat)
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return toScanJob(job), nil
}

// schemaScan reads a scan, or cancels it.
func (r *Resolver) schemaScan(ctx context.Context, id string, cancel bool) (*ScanJob, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return nil, err
	}
	scans, err := r.scans()
	if err != nil {
		return nil, err
	}
	get := scans.pool.Get
	if cancel {
		get = scans.pool.Cancel
	}
	job, ok := get(id)
	if !ok {
		return nil, nil
	}
	return toScanJob(job), nil
}
