package graph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configedit"
	"github.com/nethinwei/sql-mcp-server/x/configplan"
)

// planContext is what catalog reads and plans are evaluated against.
type planContext struct {
	// base is the draft, or the published configuration; payload is its
	// normalized form and baseID the revision a draft of it builds on.
	base      *config.Config
	payload   []byte
	baseID    int64
	published *config.Config
	sources   []configplan.Source
	// fingerprint identifies the scans read, baseHash the base payload and
	// publishedID the published revision: together, what rows depend on.
	fingerprint string
	baseHash    string
	publishedID int64
}

// planContext reads the draft (default: the published configuration) and
// every kept scan of the published configuration's datasources.
func (r *Resolver) planContext(ctx context.Context, draft *DraftInput) (planContext, error) {
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
	published, err := bootstrap.LoadRevision(rev)
	if err != nil {
		return planContext{}, err
	}
	pc := planContext{base: published, payload: rev.Payload, baseID: rev.ID, published: published, publishedID: rev.ID}
	if draft != nil {
		d, err := r.buildDraft(ctx, *draft)
		if err != nil {
			return planContext{}, err
		}
		pc.base, pc.payload, pc.baseID = d.cfg, d.payload, d.base.ID
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

// decodePlan reads a plan from its GraphQL JSON value.
func decodePlan(v any) (configplan.Plan, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return configplan.Plan{}, err
	}
	return configplan.Decode(data)
}

// denied reports the entity actions the serving connections cannot run.
func denied(caps bootstrap.EntityCapabilities) func(string, string) (string, bool) {
	return func(id, action string) (string, bool) {
		c, ok := caps[id][graphActions[Action(strings.ToUpper(action))]]
		if !ok || c.Privilege != introspect.PrivilegeDenied {
			return "", false
		}
		return c.Reason, true
	}
}

func (r *Resolver) planInput(pc planContext, review bool) configplan.Input {
	return configplan.Input{
		Base: pc.base, Published: pc.published, Sources: pc.sources, Denied: denied(r.capabilities()), Review: review,
	}
}

// planID identifies a plan with what it is evaluated against: the base
// revision, the draft and the scans read. The same ID means the same outcome.
func planID(pc planContext, p configplan.Plan) string {
	data, _ := json.Marshal(p)
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%s\x00%d", data, pc.baseID, pc.baseHash, pc.fingerprint,
		pc.publishedID))
	return hex.EncodeToString(sum[:12])
}

// evaluatePlan evaluates p against pc and validates the outcome as a whole.
func (r *Resolver) evaluatePlan(
	ctx context.Context, pc planContext, p configplan.Plan, review bool,
) (configplan.Proposal, draft, *Validation, error) {
	caps := r.capabilities()
	proposal, err := configplan.Evaluate(r.planInput(pc, review), p)
	if err != nil {
		return configplan.Proposal{}, draft{}, nil, err
	}
	cfg, payload, err := configedit.Apply(pc.payload, proposal.Edit())
	if err != nil {
		msg := err.Error()
		return proposal, draft{}, &Validation{Error: &msg, RestartRequired: []string{}, Warnings: []string{}}, nil
	}
	changes, err := r.restartChanges(ctx, cfg)
	if err != nil {
		return configplan.Proposal{}, draft{}, nil, err
	}
	hash := revision.Hash(payload)
	warnings := orEmpty(bootstrap.CapabilityWarnings(cfg, caps))
	return proposal, draft{cfg: cfg, payload: payload},
		&Validation{Ok: true, ContentHash: &hash, RestartRequired: changes, Warnings: warnings}, nil
}

func (r *Resolver) previewPlan(ctx context.Context, plan any, in *DraftInput, review bool) (*PlanPreview, error) {
	p, err := decodePlan(plan)
	if err != nil {
		return nil, err
	}
	pc, err := r.planContext(ctx, in)
	if err != nil {
		return nil, err
	}
	proposal, _, validation, err := r.evaluatePlan(ctx, pc, p, review)
	if err != nil {
		return nil, err
	}
	out := &PlanPreview{
		PlanID: planID(pc, p), Report: toPlanReport(proposal.Report), Entities: make([]Entity, 0, len(proposal.Entities)),
		Added: orEmpty(proposal.Added), Validation: validation,
	}
	for _, e := range proposal.Entities {
		out.Entities = append(out.Entities, toEntity(e))
	}
	canonical := canonicalGrants(pc.base, proposal.Config)
	out.Roles = slices.DeleteFunc(toRoles(proposal.Config), func(role Role) bool {
		_, ok := proposal.Roles[role.Name]
		canonical(role.Grants)
		return !ok
	})
	out.Users = slices.DeleteFunc(toUsers(proposal.Config), func(u User) bool {
		_, ok := proposal.Users[u.Name]
		canonical(u.Grants)
		return !ok
	})
	return out, nil
}

// canonicalGrants names the entity of each grant by its ID, as base resolved
// the reference (or, for a grant the plan added, the outcome), so the console
// can rewrite references once the entities are in its workspace.
func canonicalGrants(base, outcome *config.Config) func([]Grant) {
	before, after := config.NewEntityRefs(base.Entities), config.NewEntityRefs(outcome.Entities)
	return func(grants []Grant) {
		for i, g := range grants {
			if e, err := before.Resolve(g.Entity); err == nil {
				grants[i].Entity = e.ID()
			} else if e, err := after.Resolve(g.Entity); err == nil {
				grants[i].Entity = e.ID()
			}
		}
	}
}
