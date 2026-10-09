package configapp

import (
	"context"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
)

// SchemaList lists the schemas of datasource with what was scanned of each;
// the list is read from the database once, and again on refresh.
func (r *Service) SchemaList(ctx context.Context, datasource string, refresh bool) (*SchemaList, error) {
	cfg, scans, src, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	listed, err := scans.list(ctx, src, refresh, func() (listedSchemas, error) {
		return r.listSchemas(ctx, datasource, cfg.Databases[datasource])
	})
	if err != nil {
		return nil, err
	}
	out := &SchemaList{
		Datasource: datasource, Schemas: make([]SchemaInfo, 0, len(listed.all)),
		DefaultSchema: optional(listed.current), ListedAt: listed.at, Source: src.Token(),
	}
	names := listed.all
	if len(names) == 0 {
		names = []string{""} // the default schema of a datasource that cannot list them
	}
	kept, err := scans.kept(ctx, src)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		info := SchemaInfo{Name: name}
		if found, ok := kept[name]; ok {
			info.ScannedAt, info.Tables = &found.at, new(len(found.tables))
		}
		out.Schemas = append(out.Schemas, info)
	}
	return out, nil
}

func (r *Service) listSchemas(
	ctx context.Context, datasource string, database config.DatabaseConfig,
) (listedSchemas, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestScanTimeout)
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

// SchemaTables returns the tables of the kept ones of schemas (nil: all).
func (r *Service) SchemaTables(ctx context.Context, datasource string, schemas []string) (*Imports, error) {
	cfg, scans, src, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	return scans.importOf(ctx, src, schemas, cfg)
}

// StartSchemaScan queues a scan of schemas of datasource (none: every schema
// it lists, or its default one) and keeps what it finds.
func (r *Service) StartSchemaScan(ctx context.Context, datasource string, schemas []string) (*ScanJob, error) {
	cfg, scans, src, err := r.scanContext(ctx, datasource)
	if err != nil {
		return nil, err
	}
	job, err := scans.pool.Start(datasource, r.scanRun(cfg, scans, src, datasource, schemas))
	if err != nil {
		return nil, err
	}
	return toScanJob(job), nil
}

// SchemaScan reads a scan, or cancels it.
func (r *Service) SchemaScan(ctx context.Context, id string, cancel bool) (*ScanJob, error) {
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
	job, ok := scans.pool.Get(id)
	if !ok {
		return nil, nil
	}
	if _, err := auth.RequireDatasource(ctx, auth.PermWrite, job.Label); err != nil {
		return nil, err
	}
	job, _ = get(id)
	return toScanJob(job), nil
}

// scanRun scans schemas of datasource (none: every schema it lists, or its
// default one) and keeps what it finds. It reads the database of cfg, the
// configuration the scan was started with, which src names, even when the
// runtime reloads meanwhile.
func (r *Service) scanRun(
	cfg *config.Config, scans *Scans, src source, datasource string, schemas []string,
) func(context.Context) (struct{}, error) {
	return func(ctx context.Context) (struct{}, error) {
		var cat introspect.Catalog
		var scanned []string
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
		return struct{}{}, scans.keep(ctx, src, scanned, cat)
	}
}

// importCatalog scans schemas, or every schema of the database when none are
// named (only the default one when the introspector cannot list them), and
// returns the schemas it scanned (none for the default one).
func importCatalog(
	ctx context.Context, in introspect.Introspector, schemas []string,
) (introspect.Catalog, []string, error) {
	if lister, ok := in.(introspect.SchemaLister); ok && len(schemas) == 0 {
		all, _, err := lister.Schemas(ctx)
		if err != nil {
			return introspect.Catalog{}, nil, err
		}
		schemas = all
	}
	cat, err := LoadCatalog(ctx, in, schemas, len(schemas) == 0)
	return cat, schemas, err
}

// LoadCatalog loads a catalog that matches names the way the database
// compares them, as assembly does.
func LoadCatalog(
	ctx context.Context,
	in introspect.Introspector,
	schemas []string,
	withDefault bool,
) (introspect.Catalog, error) {
	cat, err := introspect.LoadCatalog(ctx, in, schemas, withDefault)
	if err != nil {
		return cat, err
	}
	if namer, ok := in.(introspect.PhysicalNamer); ok {
		physical, err := namer.Physical(ctx)
		if err != nil {
			return cat, err
		}
		cat.FoldCase = physical.FoldCase
	}
	return cat, nil
}
