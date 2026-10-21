package configplan

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/reason"
)

// Input is what a plan is evaluated against.
type Input struct {
	// Base is the configuration the plan changes: the console's workspace,
	// or the published configuration.
	Base *config.Config
	// Published tells published tables from those only in Base; nil: none.
	Published *config.Config
	Sources   []Source
	// Denied reports an entity action the serving connections cannot run,
	// with why; nil reports none.
	Denied func(entity, action string) (why reason.Reason, denied bool)
	// Review reports every new table a grant covers, as a recurring
	// template's proposal needs.
	Review bool
	// Missing are the entities whose tables the serving configuration found
	// missing, by ID (see GoneTables).
	Missing map[string]bool
	// MissingFields are, by entity ID, the configured fields whose columns
	// the serving configuration found missing: a sync removes them without a
	// newer scan. MissingRowColumns are the columns their row scopes test
	// (not fields) found missing: no sync repairs those.
	MissingFields, MissingRowColumns map[string][]string
	// Index, when set, is NewIndex of Sources, Base and Published: the plans
	// evaluated against them filter it rather than each resolve the entity
	// of every table again.
	Index *Index
}

// Proposal is the outcome of a plan.
type Proposal struct {
	// Config is Base with the changes applied.
	Config *config.Config
	// Entities are the entities added or changed, in configuration order.
	Entities []config.EntityConfig
	// Added are the IDs of the entities added; Removed those removed.
	Added, Removed []string
	// Roles and Users are the principals added or changed.
	Roles  map[string]config.RoleDefinition
	Users  map[string]config.UserConfig
	Report Report
}

// Empty reports a proposal that changes nothing.
func (p Proposal) Empty() bool {
	return len(p.Entities) == 0 && len(p.Removed) == 0 && len(p.Roles) == 0 && len(p.Users) == 0
}

// Report is what a reviewer reads before taking a proposal.
type Report struct {
	// Matched counts the tables in scope; Excluded those left out of them.
	Matched  int
	Excluded int
	Objects  []Objects
	Changes  []Change
	// Exceptions need a look: they may be excluded or changed.
	Exceptions []Exception
	// Unscanned are the schemas in scope never scanned, by datasource.
	Unscanned map[string][]string
	// ScannedFrom and ScannedTo bound when the tables in scope were scanned;
	// zero when unknown.
	ScannedFrom, ScannedTo time.Time
}

// Objects counts what the plan covers in one schema.
type Objects struct {
	Datasource string
	Schema     string
	Tables     int
	Imported   int
	Changed    int
}

// Change kinds.
const (
	ChangeImport     = "import"
	ChangeCreateRole = "createRole"
	ChangeGrant      = "grant"
	ChangeExclude    = "exclude"
	ChangeMask       = "mask"
	ChangeSyncAdd    = "syncAdd"
	ChangeSyncRemove = "syncRemove"
	// ChangeRemoveTable removes the entity of a table that is gone.
	ChangeRemoveTable = "removeTable"
	// ChangeSyncKeys sets an entity's keys to its table's; ChangeSyncKind
	// its kind (table or view).
	ChangeSyncKeys = "syncKeys"
	ChangeSyncKind = "syncKind"
)

// Change summarizes one kind of change over the entities it applies to.
type Change struct {
	Kind      string
	Principal string
	Actions   []string
	Mask      string
	// Fields counts the fields a field change touches.
	Fields   int
	Entities []string
}

// Exception kinds.
const (
	// ExceptionRenamed: the entity name differs from the table, which another
	// entity of the schema uses.
	ExceptionRenamed = "renamed"
	// ExceptionNoKey: no key identifies a row; updates and deletes are not
	// granted.
	ExceptionNoKey = "noKey"
	// ExceptionSideEffects: triggers or rules may write other tables.
	ExceptionSideEffects = "sideEffects"
	// ExceptionRelationshipDropped: a foreign key joins a table that has no
	// entity, or whose entity has no field for a joined column.
	ExceptionRelationshipDropped = "relationshipDropped"
	// ExceptionQualified: an existing reference would name several entities
	// once the plan adds its own; it is rewritten to the ID of the one it
	// named.
	ExceptionQualified = "qualified"
	// ExceptionUnknownUser: a grant names a user that does not exist.
	ExceptionUnknownUser = "unknownUser"
	// ExceptionNoFields: a grant's field patterns match no field.
	ExceptionNoFields = "noFields"
	// ExceptionGrantFieldsEmptied: a sync removed every field a grant lists;
	// the grant reads and writes none of its entity.
	ExceptionGrantFieldsEmptied = "grantFieldsEmptied"
	// ExceptionReferenced: a field to remove is still referenced.
	ExceptionReferenced = "referenced"
	// ExceptionDenied: the connection lacks the privilege for an action.
	ExceptionDenied = "denied"
	// ExceptionReview: a new table enters an existing grant.
	ExceptionReview = "review"
	// ExceptionKeyReferenced: a relationship joins on a column the synced
	// keys no longer include.
	ExceptionKeyReferenced = "keyReferenced"
	// ExceptionKeyNotField: a scanned key names a column the entity has no
	// field for; the sync leaves the key out.
	ExceptionKeyNotField = "keyNotField"
	// ExceptionRowColumnMissing: a row scope (tenant policy, row policy,
	// grant rows) tests a column the table lacks; the entity stays
	// unavailable until the scope is fixed by hand.
	ExceptionRowColumnMissing = "rowColumnMissing"
	// ExceptionRelationshipNew: a foreign key no relationship follows.
	ExceptionRelationshipNew = "relationshipNew"
	// ExceptionRelationshipStale: a relationship named as imported ones are
	// matches no foreign key; it may have been written by hand.
	ExceptionRelationshipStale = "relationshipStale"
)

// Exception levels: what a reviewer has to do about an exception.
const (
	// LevelBlock exceptions keep the proposal from being taken. No rule of
	// the engine raises one today; an outcome that does not validate blocks
	// the same way.
	LevelBlock = "block"
	// LevelConfirm exceptions change what is granted or fail to apply: taking
	// the proposal needs them acknowledged.
	LevelConfirm = "confirm"
	// LevelInfo exceptions only inform.
	LevelInfo = "info"
)

// levelOf is the level of an exception of kind with detail.
func levelOf(kind, detail string) string {
	switch kind {
	case ExceptionRenamed, ExceptionSideEffects, ExceptionRelationshipDropped, ExceptionQualified,
		ExceptionRelationshipNew, ExceptionRelationshipStale:
		return LevelInfo
	case ExceptionNoKey:
		if detail == "" { // reads only: nothing left out
			return LevelInfo
		}
	}
	return LevelConfirm
}

// Exception is something a reviewer should look at.
type Exception struct {
	Kind string
	// Level is block, confirm or info (see levelOf).
	Level string
	// Warning marks exceptions that are not only informative.
	Warning bool
	// Ref is the table, when excluding it is a way out.
	Ref    string
	Entity string
	// Detail names what the exception is about: a name, an action, a field.
	Detail string
	// Reason explains it, when the server does (an action denied): the
	// console translates it, Detail is its English text.
	Reason reason.Reason
}

// Evaluate applies p to in.Base. It does not validate the result: the
// caller runs it through the configuration chain.
func Evaluate(in Input, p Plan) (Proposal, error) {
	if err := p.Check(); err != nil {
		return Proposal{}, err
	}
	ev := newEvaluation(in, p)
	var rows []Row
	if in.Index != nil {
		rows = in.Index.Rows(p.Scope)
	} else {
		rows = Rows(in.Sources, in.Base, in.Published, p.Scope)
	}
	if err := checkExcluded(in, p.Exclude); err != nil {
		return Proposal{}, err
	}
	exclude := set(p.Exclude)
	rows = slices.DeleteFunc(rows, func(r Row) bool {
		excluded := exclude[r.Ref]
		if excluded {
			ev.report.Excluded++
		}
		return excluded
	})
	ev.report.Matched = len(rows)
	ev.reportScanned(rows)
	if len(p.Scope.Refs) == 0 && len(p.Scope.Entities) == 0 {
		ev.report.Unscanned = Unscanned(in.Sources, p.Scope)
	}
	targets := ev.importTables(rows)
	// References of the base the tables imported made ambiguous are
	// qualified before anything resolves them; those to the tables a sync
	// removes go at the end.
	ev.qualifyReferences()
	// Sync first, so the field rules cover the fields it adds.
	if p.Sync != nil {
		targets = ev.syncAll(targets, exclude)
	}
	for _, t := range targets {
		ev.applyFields(t)
	}
	for _, rule := range p.Grants {
		ev.grant(rule, targets)
	}
	// What the plan wrote since names entities by ID: only the references
	// to the tables a sync removed are left to go.
	if len(ev.removed) > 0 {
		ev.qualifyReferences()
	}
	return ev.proposal(rows), nil
}

// checkExcluded refuses a plan excluding a table no scan found: a misspelt
// exclusion would apply the plan to the table meant (grant it), and a
// template's to a table since dropped is to be looked at.
func checkExcluded(in Input, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	scanned := scannedRefs(in)
	gone := map[string]bool{}
	for _, g := range GoneTables(in.Sources, in.Base, Scope{}, in.Missing) { // excluded from removal
		gone[g.Ref] = true
	}
	var unknown []string
	for _, ref := range refs {
		if !scanned(ref) && !gone[ref] {
			unknown = append(unknown, ref)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("plan: exclude names no scanned table: %s (write datasource.schema.table)",
			strings.Join(unknown, ", "))
	}
	return nil
}

// scannedRefs reports whether a table (by Ref) was scanned: one the index
// has, or of the catalogs, without working out where each table stands.
func scannedRefs(in Input) func(string) bool {
	if in.Index != nil {
		return in.Index.Has
	}
	refs := map[string]bool{}
	for _, src := range in.Sources {
		for _, t := range src.Catalog.Tables {
			refs[Ref(src.Datasource, t.Schema, t.Source)] = true
		}
	}
	return func(ref string) bool { return refs[ref] }
}

// reportScanned reports when the rows were scanned, the earliest and the latest.
func (ev *evaluation) reportScanned(rows []Row) {
	for _, r := range rows {
		if r.ScannedAt.IsZero() {
			continue
		}
		if ev.report.ScannedFrom.IsZero() || r.ScannedAt.Before(ev.report.ScannedFrom) {
			ev.report.ScannedFrom = r.ScannedAt
		}
		if r.ScannedAt.After(ev.report.ScannedTo) {
			ev.report.ScannedTo = r.ScannedAt
		}
	}
}

// evaluation is the state of one Evaluate.
type evaluation struct {
	in      Input
	plan    Plan
	cfg     *config.Config
	index   map[string]int // entity ID → position in cfg.Entities
	added   []string
	removed []string
	// imported maps the Ref of each table imported to its entity.
	imported map[string]string
	changed  map[string]bool
	roles    map[string]bool
	users    map[string]bool
	report   Report
	changes  map[string]*Change
	order    []string
	// summarized tells which entities each change names, with the fields
	// it counts of each, by change key.
	summarized map[string]map[string]int
	// fieldRules are the plan's field rules, compiled.
	fieldRules []compiledFieldRule
	// refs resolves references among the entities once the plan's are
	// added; uses indexes what names each field (see fieldUses), joins the
	// relationships joining on it (see joinsOn).
	refs        *config.EntityRefs
	uses, joins map[string]map[string][]string
	// dropped are the fields a sync removed, by entity ID (see
	// dropGrantFields).
	dropped map[string]droppedFields
}

// droppedFields are the fields a sync removed from the entity of a table.
type droppedFields struct {
	ref    string
	fields []string
}

type compiledFieldRule struct {
	FieldRule
	columns, types globs
}

func newEvaluation(in Input, p Plan) *evaluation {
	cfg := *in.Base
	cfg.Entities = slices.Clone(in.Base.Entities)
	cfg.Roles = maps.Clone(in.Base.Roles)
	cfg.Users = maps.Clone(in.Base.Users)
	if cfg.Roles == nil {
		cfg.Roles = map[string]config.RoleDefinition{}
	}
	ev := &evaluation{
		in: in, plan: p, cfg: &cfg, index: map[string]int{}, imported: map[string]string{}, changed: map[string]bool{},
		roles: map[string]bool{}, users: map[string]bool{}, changes: map[string]*Change{},
		summarized: map[string]map[string]int{}, dropped: map[string]droppedFields{},
	}
	for _, r := range p.Fields {
		ev.fieldRules = append(ev.fieldRules, compiledFieldRule{r, compileGlobs(r.Columns), compileGlobs(r.Types)})
	}
	for i, e := range cfg.Entities {
		ev.index[e.ID()] = i
	}
	return ev
}

// target is an entity the plan applies to, with the table it reads.
type target struct {
	row Row
	id  string
	new bool
}

func (ev *evaluation) entity(t target) *config.EntityConfig { return &ev.cfg.Entities[ev.index[t.id]] }

// change marks an entity changed, copying the slices a rule will modify.
func (ev *evaluation) change(t target) *config.EntityConfig {
	e := ev.entity(t)
	if !ev.changed[t.id] {
		ev.changed[t.id] = true
		e.Fields = slices.Clone(e.Fields)
	}
	return e
}

func (ev *evaluation) except(x Exception) {
	x.Level = levelOf(x.Kind, x.Detail)
	x.Warning = x.Level != LevelInfo
	ev.report.Exceptions = append(ev.report.Exceptions, x)
}

// summarize adds an entity to the change of its kind and details.
func (ev *evaluation) summarize(c Change, entity string, fields int) {
	key := strings.Join(append([]string{c.Kind, c.Principal, c.Mask}, c.Actions...), "\x00")
	got, ok := ev.changes[key]
	if !ok {
		got = &c
		ev.changes[key] = got
		ev.order = append(ev.order, key)
	}
	got.Fields += fields
	if entity == "" {
		return
	}
	if ev.summarized[key] == nil {
		ev.summarized[key] = map[string]int{}
	}
	if _, ok := ev.summarized[key][entity]; !ok {
		got.Entities = append(got.Entities, entity)
	}
	ev.summarized[key][entity] += fields
}

// forget takes the entities of drop out of the report: the changes naming
// them (a change left naming none goes) and their exceptions.
func (ev *evaluation) forget(drop map[string]bool) {
	ev.order = slices.DeleteFunc(ev.order, func(key string) bool {
		c, named := ev.changes[key], ev.summarized[key]
		if len(c.Entities) == 0 {
			return false
		}
		c.Entities = slices.DeleteFunc(c.Entities, func(id string) bool {
			if drop[id] {
				c.Fields -= named[id]
				delete(named, id)
			}
			return drop[id]
		})
		if len(c.Entities) > 0 {
			return false
		}
		delete(ev.changes, key)
		delete(ev.summarized, key)
		return true
	})
	ev.report.Exceptions = slices.DeleteFunc(ev.report.Exceptions, func(x Exception) bool { return drop[x.Entity] })
}
