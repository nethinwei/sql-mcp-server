package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/audit"
	"github.com/nethinwei/sql-mcp-server/budget"
	"github.com/nethinwei/sql-mcp-server/cache"
	"github.com/nethinwei/sql-mcp-server/codegen"
	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/cost"
	"github.com/nethinwei/sql-mcp-server/dialect"
	"github.com/nethinwei/sql-mcp-server/engine"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/hook"
	"github.com/nethinwei/sql-mcp-server/mask"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/relalg"
	"github.com/nethinwei/sql-mcp-server/store"
)

// Sentinel errors. Business errors (Unauthorized/NotFound/UnsafeWrite/Cost)
// are mapped by x/mcpserver to IsError results; protocol errors to JSON-RPC.
var (
	// ErrEntityNotFound is returned when the requested entity is unknown.
	ErrEntityNotFound = errors.New("tool: entity not found")
	// ErrUnauthorized is returned when the role lacks the action.
	ErrUnauthorized = errors.New("tool: unauthorized")
	// ErrInvalidInput is returned for malformed tool input.
	ErrInvalidInput = errors.New("tool: invalid input")
	// ErrDMLToolsDisabled is returned when an entity is not exposed to the
	// generic entity tools.
	ErrDMLToolsDisabled = errors.New("tool: entity DML tools disabled")
	// ErrUnsafeWrite is returned when an update/delete lacks a PK-scoped filter.
	ErrUnsafeWrite = errors.New("tool: unsafe write without primary-key filter")
	// ErrNotImplemented is returned by stub tools.
	ErrNotImplemented = errors.New("tool: not implemented")
	// ErrDuplicateTool is returned by NewRegistry for duplicate tool names.
	ErrDuplicateTool = errors.New("tool: duplicate name")
	// ErrDatabase identifies errors returned by the database driver.
	ErrDatabase = errors.New("tool: database error")
)

type databaseError struct{ err error }

func (e *databaseError) Error() string { return ErrDatabase.Error() }
func (e *databaseError) Unwrap() error { return e.err }
func (e *databaseError) Is(target error) bool {
	return target == ErrDatabase
}

// WrapDBError classifies a database driver error while preserving its cause.
func WrapDBError(err error) error {
	if err == nil || errors.Is(err, ErrDatabase) {
		return err
	}
	return &databaseError{err: err}
}

// Info is a tool's static metadata, mapped to an MCP tool definition.
type Info struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	ReadOnly    bool
}

// Result is a tool's outcome. Content holds structured rows/text; IsError marks
// a business-level error the agent can act on.
type Result struct {
	Content              []map[string]any
	IsError              bool
	StructuredResult     any
	ReturnedRows         int64
	ReturnedBytes        int64
	EstimatedScannedRows int64
}

// Context carries per-request dependencies (injected, no mutable global state).
type Context struct {
	Role                       string
	Subject                    map[string]any // caller attributes for row-level ${subject.x} policies
	Session                    string         // MCP session ID when the transport provides one
	DB                         store.DB
	Dialect                    dialect.Dialect
	Registry                   *entity.Registry
	Authorizer                 rbac.Authorizer
	Masker                     mask.Masker
	Gate                       cost.Gate
	Cache                      cache.Cache[[]map[string]any]
	Engine                     *engine.Engine
	Auditor                    audit.Auditor
	Hooks                      *hook.Hooks
	Timeout                    time.Duration
	MaxRows                    int64
	MaxProcedureRows           int64
	MaxReturnedBytes           int64
	MaxINListSize              int
	MaxFilterConditions        int
	MaxGroupByFields           int
	MaxAggregates              int
	MaxExpand                  int
	CacheMaxEntryRows          int
	CacheMaxEntryBytes         int64
	TransactionBeginTimeout    time.Duration
	TransactionCommitTimeout   time.Duration
	TransactionRollbackTimeout time.Duration
	Feedback                   cost.FeedbackStore
	Analyze                    cost.AnalyzePolicy
	DataSource                 string
	Sources                    map[string]DataSource
	Budget                     budget.Manager
	BudgetLimits               budget.Limits
	Transactions               *TransactionManager
	TxBeginners                map[string]store.TxBeginner
	Transaction                string
}

// DataSource is the per-entity execution route.
type DataSource struct {
	DB      store.DB
	Dialect dialect.Dialect
	Gate    cost.Gate
	Analyze cost.AnalyzePolicy
}

// Tool is a single DML capability.
type Tool interface {
	Info() Info
	Enabled(flags config.ToolFlags) bool
	Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error)
}

// CostGated marks a tool whose execution must pass the cost gate.
type CostGated interface {
	Tool
	CostGated()
}

// RunTool executes a tool with the cross-cutting wiring that must apply to
// every call regardless of transport: lifecycle hooks, bounded concurrency +
// backpressure + circuit breaking + (for read-only tools) singleflight via the
// engine, and best-effort audit. Transports (x/mcpserver) call this instead of
// Tool.Run directly, keeping the wiring in the core where it is unit-testable.
// A nil Engine/Auditor/Hooks each degrade to a no-op.
func RunTool(ctx context.Context, t Tool, input json.RawMessage, tc Context) (Result, error) {
	name := t.Info().Name
	start := time.Now()
	auditInput := audit.RedactInput(input, sensitiveFields(name, input, tc.Registry))
	var lease budget.Lease
	if tc.Budget != nil {
		var err error
		scope := budget.Scope{
			Role: tc.Role, Tenant: tenantKey(tc.Subject), Session: tc.Session,
		}
		if manager, ok := tc.Budget.(budget.ReservingManager); ok {
			reserved := int64(1)
			switch name {
			case "read_records", "aggregate_records":
				reserved = tc.MaxRows
			case "execute_entity":
				reserved = tc.MaxProcedureRows
			}
			if strings.HasPrefix(name, "procedure_") {
				reserved = tc.MaxProcedureRows
			}
			if reserved <= 0 {
				reserved = 1
			}
			lease, err = manager.AcquireWithReservation(ctx, scope, budget.Reservation{Cost: reserved})
		} else {
			lease, err = tc.Budget.Acquire(ctx, scope)
		}
		if err != nil {
			if tc.Auditor != nil {
				_ = tc.Auditor.Record(ctx, audit.Event{
					Time: time.Now(), Role: tc.Role, Tool: name, Input: auditInput,
					Allowed: false, Error: err.Error(),
				})
			}
			return Result{}, err
		}
		ctx = lease.Context()
		tc.BudgetLimits = lease.Limits()
	}
	ctx = tc.Hooks.FireBeforeTool(ctx, name, input)
	var res Result
	var err error
	if tc.Engine != nil {
		// Only read-only tools are de-duplicated, and the key includes role and
		// subject so callers with different row-level scopes never share a
		// result. Transactional calls are never coalesced: validate the token
		// identity before engine admission, then execute against its own Tx.
		key := ""
		if t.Info().ReadOnly {
			transaction := transactionFromInput(input)
			if transaction != "" {
				if tc.Transactions == nil {
					err = ErrTransactionNotFound
				} else {
					err = tc.Transactions.Validate(transaction, tc.Session, tc.Role, tc.Subject)
				}
			} else {
				key = name + "\x00" + scopeKey(tc.Role, tc.Subject) + "\x00" + string(input)
			}
		}
		if err == nil {
			var val any
			val, err = tc.Engine.Submit(ctx, key, func(ctx context.Context) (any, error) {
				return t.Run(ctx, input, tc)
			})
			if r, ok := val.(Result); ok {
				res = r
			}
		}
	} else {
		res, err = t.Run(ctx, input, tc)
	}
	if encoded, encodeErr := json.Marshal(res.Content); encodeErr == nil {
		res.ReturnedBytes = int64(len(encoded))
		if tc.MaxReturnedBytes > 0 && res.ReturnedBytes > tc.MaxReturnedBytes && err == nil {
			err = budget.ErrExceeded
		}
	}
	if lease != nil {
		returnedRows := res.ReturnedRows
		if returnedRows == 0 {
			returnedRows = int64(len(res.Content))
		}
		budgetErr := lease.Complete(budget.Usage{
			EstimatedScannedRows: res.EstimatedScannedRows,
			ReturnedRows:         returnedRows,
			ReturnedBytes:        res.ReturnedBytes,
			Duration:             time.Since(start),
			Cost:                 returnedRows + time.Since(start).Milliseconds(),
		})
		if err == nil && budgetErr != nil {
			err = budgetErr
		}
	}
	tc.Hooks.FireAfterTool(ctx, name, res.StructuredResult, err)
	if err != nil {
		tc.Hooks.FireOnError(ctx, err)
	}
	if tc.Auditor != nil {
		_ = tc.Auditor.Record(ctx, audit.Event{
			Time:         time.Now(),
			Role:         tc.Role,
			Tool:         name,
			Input:        auditInput,
			Allowed:      err == nil,
			Error:        errString(err),
			Duration:     time.Since(start),
			ReturnedRows: returnedRowsForAudit(res),
		})
	}
	return res, err
}

func returnedRowsForAudit(res Result) int64 {
	if res.ReturnedRows > 0 {
		return res.ReturnedRows
	}
	return int64(len(res.Content))
}

func sensitiveFields(toolName string, input json.RawMessage, registry *entity.Registry) map[string]bool {
	var envelope struct {
		Entity string `json:"entity"`
	}
	if registry == nil || decodeInput(input, &envelope) != nil {
		return nil
	}
	entityName := envelope.Entity
	if entityName == "" {
		for _, candidate := range registry.Entities() {
			if candidate.Kind == entity.KindProcedure && ProcedureToolName(candidate.Name) == toolName {
				entityName = candidate.Name
				break
			}
		}
	}
	if entityName == "" {
		return nil
	}
	resolved, ok := registry.Resolve(entityName)
	if !ok {
		return nil
	}
	fields := make(map[string]bool)
	for _, attribute := range resolved.Attributes {
		if attribute.Mask == "" {
			continue
		}
		fields[attribute.Name] = true
		if attribute.Alias != "" {
			fields[attribute.Alias] = true
		}
	}
	return fields
}

func transactionFromInput(input json.RawMessage) string {
	var envelope struct {
		Transaction string `json:"transaction"`
	}
	_ = decodeInput(input, &envelope)
	return envelope.Transaction
}

func tenantKey(subject map[string]any) string {
	for _, key := range []string{"tenant", "tenant_id", "tenantID"} {
		if value, ok := subject[key]; ok {
			return fmt.Sprint(value)
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Registry holds an immutable set of tools.
type Registry struct {
	tools  []Tool
	byName map[string]Tool
}

// NewRegistry builds a Registry, rejecting duplicate tool names.
func NewRegistry(tools []Tool) (*Registry, error) {
	r := &Registry{byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		name := t.Info().Name
		if _, ok := r.byName[name]; ok {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateTool, name)
		}
		r.byName[name] = t
		r.tools = append(r.tools, t)
	}
	return r, nil
}

// Tools returns all registered tools.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, len(r.tools))
	copy(out, r.tools)
	return out
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// Enabled returns the tools whose Enabled(flags) is true.
func (r *Registry) Enabled(flags config.ToolFlags) []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if t.Enabled(flags) {
			out = append(out, t)
		}
	}
	return out
}

// ---- input shapes ----

type condJSON struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value any    `json:"value"`
}

type aggJSON struct {
	Func  string `json:"func"`
	Field string `json:"field"`
}

type readInput struct {
	Entity      string         `json:"entity"`
	Transaction string         `json:"transaction,omitempty"`
	Fields      []string       `json:"fields,omitempty"`
	Expand      []string       `json:"expand,omitempty"`
	Filter      []condJSON     `json:"filter,omitempty"`
	Limit       int64          `json:"limit,omitempty"`
	Offset      int64          `json:"offset,omitempty"`
	Cursor      map[string]any `json:"cursor,omitempty"`
}

type createInput struct {
	Entity      string         `json:"entity"`
	Transaction string         `json:"transaction,omitempty"`
	Values      map[string]any `json:"values"`
}

type updateInput struct {
	Entity      string         `json:"entity"`
	Transaction string         `json:"transaction,omitempty"`
	Filter      []condJSON     `json:"filter"`
	Set         map[string]any `json:"set"`
}

type deleteInput struct {
	Entity      string     `json:"entity"`
	Transaction string     `json:"transaction,omitempty"`
	Filter      []condJSON `json:"filter"`
}

type aggregateInput struct {
	Entity      string     `json:"entity"`
	Transaction string     `json:"transaction,omitempty"`
	GroupBy     []string   `json:"groupBy,omitempty"`
	Aggregates  []aggJSON  `json:"aggregates"`
	Filter      []condJSON `json:"filter,omitempty"`
}

type executeInput struct {
	Entity      string         `json:"entity"`
	Args        map[string]any `json:"args,omitempty"`
	Transaction string         `json:"transaction,omitempty"`
}

// ---- helpers ----

func filterToPredicate(conds []condJSON) (relalg.Predicate, error) {
	if len(conds) == 0 {
		return nil, nil
	}
	var preds []relalg.Predicate
	for _, c := range conds {
		op := relalg.Op(c.Op)
		if !op.Valid() {
			return nil, fmt.Errorf("%w: operator %q", ErrInvalidInput, c.Op)
		}
		value, err := normalizeJSONValue(c.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		preds = append(preds, relalg.Condition{Field: c.Field, Op: op, Value: value})
	}
	if len(preds) == 1 {
		return preds[0], nil
	}
	return relalg.And{Preds: preds}, nil
}

func decodeInput(data json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func normalizeMapValues(values map[string]any) error {
	for key, value := range values {
		normalized, err := normalizeJSONValue(value)
		if err != nil {
			return err
		}
		values[key] = normalized
	}
	return nil
}

func normalizeJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer, nil
		}
		if strings.ContainsAny(typed.String(), ".eE") {
			floating, err := typed.Float64()
			if err != nil {
				return nil, fmt.Errorf("invalid JSON number %q", typed)
			}
			return floating, nil
		}
		return nil, fmt.Errorf("integer %q exceeds int64; encode it as a JSON string", typed)
	case []any:
		for i, item := range typed {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			typed[i] = normalized
		}
		return typed, nil
	case map[string]any:
		if err := normalizeMapValues(typed); err != nil {
			return nil, err
		}
		return typed, nil
	default:
		return value, nil
	}
}

func andPreds(a, b relalg.Predicate) relalg.Predicate {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return relalg.And{Preds: []relalg.Predicate{a, b}}
}

// keysetAfter builds a strict row-value comparison predicate that resumes after
// the cursor position for keyset pagination, plus the ordered key columns to
// sort by. For a composite key (a,b) it expands (a,b) > (va,vb) into
// `a>va OR (a=va AND b>vb)`; a naive `a>va AND b>vb` would skip rows. It uses
// the longest cursor-provided prefix of pks so ORDER BY matches the predicate.
func keysetAfter(pks []string, cursor map[string]any) (relalg.Predicate, []string) {
	var cols []string
	var vals []any
	for _, pk := range pks {
		v, ok := cursor[pk]
		if !ok {
			break
		}
		cols = append(cols, pk)
		vals = append(vals, v)
	}
	if len(cols) == 0 {
		return nil, nil
	}
	ors := make([]relalg.Predicate, 0, len(cols))
	for i := range cols {
		ands := make([]relalg.Predicate, 0, i+1)
		for j := 0; j < i; j++ {
			ands = append(ands, relalg.Condition{Field: cols[j], Op: relalg.OpEq, Value: vals[j]})
		}
		ands = append(ands, relalg.Condition{Field: cols[i], Op: relalg.OpGt, Value: vals[i]})
		if len(ands) == 1 {
			ors = append(ors, ands[0])
		} else {
			ors = append(ors, relalg.And{Preds: ands})
		}
	}
	if len(ors) == 1 {
		return ors[0], cols
	}
	return relalg.Or{Preds: ors}, cols
}

func toExceededError(d cost.Decision) error {
	p := cost.Plan{}
	if d.Plan != nil {
		p = *d.Plan
	}
	s := cost.Score{}
	if d.Score != nil {
		s = *d.Score
	}
	return &cost.ExceededError{Plan: p, Score: s, Hints: d.Hints, Soft: d.Soft}
}

func authorize(ctx context.Context, tc Context, req rbac.Request) (rbac.Decision, error) {
	dec, err := tc.Authorizer.Authorize(ctx, req)
	tc.Hooks.FireAuthorize(ctx, req, dec)
	return dec, err
}

func checkGate(ctx context.Context, tc Context, compiled codegen.Compiled) (codegen.Compiled, error) {
	checked, _, err := checkGateDetailed(ctx, tc, compiled)
	return checked, err
}

func checkGateDetailed(ctx context.Context, tc Context, compiled codegen.Compiled) (codegen.Compiled, *cost.Plan, error) {
	if tc.Gate == nil {
		return compiled, nil, nil
	}
	dec, err := tc.Gate.Check(ctx, compiled)
	if err != nil {
		tc.Hooks.FireCostGate(ctx, cost.Plan{}, cost.Score{}, "hard")
		return compiled, nil, err
	}
	plan, score := cost.Plan{}, cost.Score{}
	if dec.Plan != nil {
		plan = *dec.Plan
	}
	if dec.Score != nil {
		score = *dec.Score
	}
	maxEstimated := tc.BudgetLimits.MaxEstimatedScannedRows
	if maxEstimated == 0 {
		maxEstimated = tc.BudgetLimits.MaxScannedRows
	}
	if dec.Plan != nil && maxEstimated > 0 &&
		dec.Plan.EstimatedRows > maxEstimated {
		return compiled, dec.Plan, budget.ErrExceeded
	}
	verdict := "allow"
	if dec.Soft {
		verdict = "soft"
	} else if !dec.Allow {
		verdict = "hard"
	}
	tc.Hooks.FireCostGate(ctx, plan, score, verdict)
	if !dec.Allow {
		return compiled, dec.Plan, toExceededError(dec)
	}
	if dec.Rewritten != nil {
		compiled = *dec.Rewritten
	}
	return compiled, dec.Plan, nil
}

func maskRow(m mask.Masker, attrs []entity.Attribute, row map[string]any) {
	rules := make(map[string]string, len(attrs))
	for _, a := range attrs {
		if a.Mask != "" {
			rules[a.Name] = a.Mask
		}
	}
	for f, v := range row {
		if rule, ok := rules[f]; ok {
			if mv, err := m.Mask(rule, v); err == nil {
				row[f] = mv
			}
		}
	}
}

// argsKey builds a collision-resistant cache-key fragment from bound args.
// fmt.Sprint collides across type/boundary (e.g. ["a b","c"] vs ["a","b c"],
// or "1" vs 1), which under row-level security could cross-serve rows; JSON
// preserves string boundaries and the string-vs-number distinction.
func argsKey(args []any) string {
	if b, err := json.Marshal(args); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%#v", args)
}

func scopeKey(role string, subject map[string]any) string {
	if b, err := json.Marshal(subject); err == nil {
		return role + "\x00" + string(b)
	}
	return role + "\x00" + fmt.Sprintf("%#v", subject)
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// filterFields extracts the field names referenced by a filter.
func filterFields(conds []condJSON) []string {
	out := make([]string, 0, len(conds))
	for _, c := range conds {
		out = append(out, c.Field)
	}
	return out
}

// validateFields rejects any field name that is not a visible attribute of the
// entity (by name or alias). This closes a side channel where a filter, GROUP
// BY, or write could reference an excluded/hidden column — e.g. filtering on a
// masked salary to infer its value. Unknown and excluded fields are treated
// identically so the error never reveals which columns exist.
func validateFields(res entity.Resolved, fields ...string) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(res.Attributes)*2)
	for _, a := range res.Attributes {
		allowed[a.Name] = true
		if a.Alias != "" {
			allowed[a.Alias] = true
		}
	}
	for _, f := range fields {
		if !allowed[f] {
			return fmt.Errorf("%w: unknown or inaccessible field %q", ErrInvalidInput, f)
		}
	}
	return nil
}

// validateUnmaskedFields rejects masked fields in usages that can reveal or
// compare their underlying values. It intentionally uses the same error as an
// unknown field so callers cannot probe whether a field is masked.
func validateUnmaskedFields(res entity.Resolved, fields ...string) error {
	for _, f := range fields {
		for _, a := range res.Attributes {
			if (a.Name == f || a.Alias == f) && a.Mask != "" {
				return fmt.Errorf("%w: unknown or inaccessible field %q", ErrInvalidInput, f)
			}
		}
	}
	return nil
}

func resolveDMLEntity(tc Context, name string) (entity.Resolved, error) {
	res, ok := tc.Registry.Resolve(name)
	if !ok {
		return entity.Resolved{}, ErrEntityNotFound
	}
	if !res.Entity.MCP.DMLTools {
		return entity.Resolved{}, ErrDMLToolsDisabled
	}
	return res, nil
}

func routeEntity(tc Context, e entity.Entity) (Context, error) {
	name := e.DataSource
	if name == "" {
		name = "default"
	}
	tc.DataSource = name
	if len(tc.Sources) == 0 {
		return tc, nil
	}
	source, ok := tc.Sources[name]
	if !ok {
		return tc, fmt.Errorf("%w: datasource %q", ErrEntityNotFound, name)
	}
	tc.DB, tc.Dialect, tc.Gate, tc.Analyze = source.DB, source.Dialect, source.Gate, source.Analyze
	if tc.Transaction != "" {
		if tc.Transactions == nil {
			return tc, ErrTransactionNotFound
		}
		db, err := tc.Transactions.DB(tc.Transaction, tc.Session, tc.Role, tc.Subject, name)
		if err != nil {
			return tc, err
		}
		tc.DB = db
	}
	return tc, nil
}

func afterWrite(tc Context, e entity.Entity, transaction string) error {
	if transaction == "" {
		if tc.Cache != nil {
			return tc.Cache.Invalidate(e.Name)
		}
		return nil
	}
	if tc.Transactions == nil {
		return ErrTransactionNotFound
	}
	datasource := e.DataSource
	if datasource == "" {
		datasource = "default"
	}
	return tc.Transactions.MarkDirty(transaction, tc.Session, tc.Role, tc.Subject, datasource, e.Name)
}

// withTimeout returns a context bounded by tc.Timeout, or ctx unchanged when
// no timeout is configured. The cancel func is always non-nil.
func withTimeout(ctx context.Context, tc Context) (context.Context, context.CancelFunc) {
	if tc.Timeout > 0 {
		return context.WithTimeout(ctx, tc.Timeout)
	}
	return ctx, func() {}
}

func withSpecificTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
}
