package configapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/reason"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// planContext is what catalog reads and plans are evaluated against.
type planContext struct {
	// base is the draft, or the published configuration; payload is its
	// normalized form and baseID the revision a draft of it builds on.
	base      *config.Config
	spec      *config.Spec
	payload   []byte
	baseID    int64
	published *config.Config
	sources   []configplan.Source
	// fingerprint identifies the scans read, baseHash the base payload and
	// publishedID the published revision: together, what rows depend on.
	fingerprint string
	baseHash    string
	publishedID int64
	// missing is what the serving configuration found missing (see
	// configplan.GoneTables and configplan.Input).
	missing bootstrap.Missing
}

// planContext reads the draft (default: the published configuration) and
// every kept scan of the published configuration's datasources.
func (r *Service) planContext(ctx context.Context, draft *Draft) (planContext, error) {
	scans, err := r.scans()
	if err != nil {
		return planContext{}, err
	}
	rev, err := r.Store.Published(ctx)
	if errors.Is(err, revision.ErrNoPublished) {
		return planContext{}, errors.New("nothing is published: datasources come from the published configuration")
	}
	if err != nil {
		return planContext{}, err
	}
	spec, published, err := bootstrap.LoadRevisionSpec(rev)
	if err != nil {
		return planContext{}, err
	}
	pc := planContext{
		base: published, spec: spec, payload: rev.Payload, baseID: rev.ID, published: published, publishedID: rev.ID,
		missing: r.missing(),
	}
	if draft != nil {
		d, err := r.BuildDraft(ctx, *draft)
		if err != nil {
			return planContext{}, err
		}
		pc.base, pc.spec, pc.payload, pc.baseID = d.Config, d.Spec, d.Payload, d.Base.ID
	}
	pc.baseHash = revision.Hash(pc.payload)
	pc.sources, pc.fingerprint, err = scans.sources(ctx, published)
	return pc, err
}

// sources reads what was kept of every datasource of cfg, with a
// fingerprint that changes whenever a schema is scanned or listed again.
func (s *Scans) sources(ctx context.Context, cfg *config.Config) ([]configplan.Source, string, error) {
	names := make([]string, 0, len(cfg.Databases))
	for name := range cfg.Databases {
		names = append(names, name)
	}
	sort.Strings(names)
	sum := sha256.New()
	out := make([]configplan.Source, 0, len(names))
	for _, name := range names {
		src := sourceOf(cfg, name)
		cat, kept, err := s.catalogOf(ctx, src)
		if err != nil {
			return nil, "", err
		}
		listed, ok, err := s.store.CatalogList(ctx, src.ID())
		if err != nil {
			return nil, "", err
		}
		source := configplan.Source{Datasource: name, Catalog: cat, ScannedAt: map[string]time.Time{}}
		_, _ = fmt.Fprintf(sum, "%s\x00%s\x00%d\x00", name, src.ID(), listed.ListedAt.UnixMicro())
		if ok {
			source.Listed = listed.Schemas
		}
		for schema, k := range kept {
			source.Scanned = append(source.Scanned, schema)
			source.ScannedAt[schema] = k.at
		}
		sort.Strings(source.Scanned)
		for _, schema := range source.Scanned {
			_, _ = fmt.Fprintf(sum, "%s=%d\x00", schema, kept[schema].at.UnixMicro())
		}
		out = append(out, source)
	}
	return out, hex.EncodeToString(sum.Sum(nil)[:12]), nil
}

// AuthorizePlanning refuses a caller who may not scope, preview or apply
// plans (admin:read and admin:write), before their input is even decoded.
func AuthorizePlanning(ctx context.Context) error {
	for _, perm := range []string{auth.PermRead, auth.PermWrite} {
		if _, err := auth.Require(ctx, perm); err != nil {
			return err
		}
	}
	return nil
}

// DecodePlan reads a plan from its JSON value.
func DecodePlan(v any) (configplan.Plan, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return configplan.Plan{}, err
	}
	return configplan.Decode(data)
}

// denied reports the entity actions the serving connections cannot run.
func denied(caps bootstrap.EntityCapabilities) func(string, string) (reason.Reason, bool) {
	return func(id, action string) (reason.Reason, bool) {
		a, known := entity.ParseAction(strings.ToLower(action))
		c, ok := caps[id][a]
		if !known || !ok || c.Privilege != introspect.PrivilegeDenied {
			return reason.Reason{}, false
		}
		return c.Reason, true
	}
}

func (r *Service) planInput(pc planContext, review bool) configplan.Input {
	return configplan.Input{
		Base: pc.base, Published: pc.published, Sources: pc.sources, Denied: denied(r.capabilities()), Review: review,
		Missing: pc.missing.Tables, MissingFields: pc.missing.Fields, MissingRowColumns: pc.missing.RowColumns,
	}
}

// planID identifies a plan with what it is evaluated against (the base
// revision, the draft and the scans read) and how (as a review or not). The
// same ID means the same outcome and report.
func planID(pc planContext, p configplan.Plan, review bool) string {
	data, _ := json.Marshal(p)
	missing, _ := json.Marshal(pc.missing) // maps marshal with sorted keys
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%s\x00%d\x00%t\x00%s", data, pc.baseID, pc.baseHash,
		pc.fingerprint, pc.publishedID, review, missing))
	return hex.EncodeToString(sum[:12])
}

// evaluate evaluates p against pc and validates the outcome as a whole.
func (r *Service) evaluate(ctx context.Context, pc planContext, p configplan.Plan, review bool) (*Preview, error) {
	proposal, err := configplan.Evaluate(r.planInput(pc, review), p)
	if err != nil {
		return nil, err
	}
	out := &Preview{PlanID: planID(pc, p, review), Plan: p, Review: review, Proposal: proposal, Base: pc.base,
		Report: toPlanReport(proposal.Report)}
	_, cfg, payload, err := configedit.ApplySpec(pc.spec, proposal.Edit())
	if err != nil {
		msg := err.Error()
		out.Validation = &Validation{Error: &msg, RestartRequired: []string{}, Warnings: []string{}, Notices: []Reason{}}
		return out, nil
	}
	changes, err := r.RestartChanges(ctx, cfg)
	if err != nil {
		return nil, err
	}
	hash := revision.Hash(payload)
	list, warnings := notices(bootstrap.CapabilityWarnings(cfg, r.capabilities()))
	out.Validation = &Validation{Ok: true, ContentHash: &hash, RestartRequired: changes, Warnings: warnings,
		Notices: list}
	out.changed, out.effect, err = r.changes(pc, payload)
	return out, err
}
