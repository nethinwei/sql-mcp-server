package tool

import (
	"context"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/budget"
	"github.com/nethinwei/sql-mcp-server/core/cache"
	"github.com/nethinwei/sql-mcp-server/core/codegen"
	"github.com/nethinwei/sql-mcp-server/core/cost"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

type readPlan struct {
	entityPlan
	projectionFields []string
	full             relalg.Predicate
	keysetCols       []string // ORDER BY for keyset pagination
	order            readOrder
	rel              related
	grant            grant
}

func runRead(ctx context.Context, tc Context, in readInput) (Result, error) {
	ctx, tc, end := beginCall(ctx, tc, true, in.Transaction)
	defer end()
	start := time.Now()
	capped := capLimit(tc, &in.Limit)
	plan, err := prepareReadPlan(ctx, tc, in)
	if err != nil {
		return Result{}, err
	}
	st, planTemplate, cacheKey, err := compileReadQuery(ctx, plan, in)
	if err != nil {
		return Result{}, err
	}
	var stamp uint64 // invalidations before the query starts, see cache.Cache.Stamp
	if plan.tc.Cache != nil {
		stamp = plan.tc.Cache.Stamp(cacheKey.Database)
	}
	// A read across relationships is not cached: writes to their targets
	// would not invalidate it.
	cacheable := len(plan.rel.joins) == 0 && !relalg.HasExists(plan.full)
	if cacheable {
		if cached, err, ok := readCacheHit(ctx, plan.tc, in, cacheKey); ok {
			return cached, err
		}
	}
	st.keep = expandJoinColumns(plan.res.Entity, in.Expand)
	_, before := plan.tc.results.counted()
	got, err := st.query(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := expandRows(ctx, plan.tc, plan.res.Entity, got, in.Expand); err != nil {
		return Result{}, err
	}
	// The cursor first: an ordered field may be a join column the read adds.
	next := nextCursor(plan.order, plan.res, in, got.rows)
	if err := finishReadRows(plan, in, got.rows, before); err != nil {
		return Result{}, err
	}
	recordReadFeedback(ctx, plan.tc, in.Entity, st.sql.Compiled, planTemplate, st.sql.plan,
		int64(len(got.rows)), time.Since(start))
	// Rows left past the cap that no cursor reaches are reported.
	truncated := next == nil && capped && int64(len(got.rows)) >= in.Limit
	if next == nil && !truncated && cacheable {
		storeReadCache(ctx, plan.tc, in, cacheKey, stamp, got.rows)
	}
	returned, _ := plan.tc.results.counted()
	return Result{Content: got.rows, ReturnedRows: returned, EstimatedScannedRows: st.sql.estimatedRows(),
		NextCursor: next, Truncated: truncated}, nil
}

// finishReadRows trims from rows what the read added to them for itself, and
// counts the bytes an expansion repeats (see resultMeter.grow): before is
// what the call had counted before the read.
func finishReadRows(plan readPlan, in readInput, rows []map[string]any, before int64) error {
	trimExpandedJoinFields(plan.res.Entity, in, rows)
	trimOrderFields(plan.order, plan.res, in, rows)
	if len(in.Expand) == 0 {
		return nil
	}
	return plan.tc.results.grow(before, rows)
}

// capLimit bounds *limit by tc.MaxRows, the rows a read or an aggregate
// returns at most (see cost.EnforceCap): a call asking for more, or for no
// limit, reads that many, and reports whether it did (a full page of them
// may not be all).
func capLimit(tc Context, limit *int64) bool {
	if tc.MaxRows <= 0 || *limit < 0 || *limit > 0 && *limit <= tc.MaxRows { // a negative one is refused later
		return false
	}
	*limit = tc.MaxRows
	return true
}

func prepareReadPlan(ctx context.Context, tc Context, in readInput) (readPlan, error) {
	ep, pred, err := planEntity(ctx, tc, in.Entity, entity.ActionRead, in.Filter)
	if err != nil {
		return readPlan{}, err
	}
	res, tc := ep.res, ep.tc
	readFields, projectionFields, err := readFieldNames(res, in)
	if err != nil {
		return readPlan{}, err
	}
	if _, related := splitPaths(res.Entity, in.Fields); len(related) > 0 {
		return readPlan{}, fmt.Errorf("%w: fields are the entity's own (%s is related): read related rows with "+
			"expand", ErrInvalidInput, related[0])
	}
	readFields, paths := splitPaths(res.Entity, readFields)
	_, orderPaths := splitPaths(res.Entity, orderFieldNames(in.OrderBy))
	rel, pred, locals, err := relate(ctx, tc, res, pred, append(paths, orderPaths...))
	if err != nil {
		return readPlan{}, err
	}
	readFields = append(readFields, locals...)
	if err := checkReadUses(tc, res, in, readFields, locals); err != nil {
		return readPlan{}, err
	}
	req := rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: in.Entity, Action: entity.ActionRead,
		Fields: projectionFields, ReadFields: readFields, Predicate: pred,
	}
	order, req, err := orderRead(ctx, tc, res, in, req, rel)
	if err != nil {
		return readPlan{}, err
	}
	g, err := authorizeRead(ctx, tc, res, in, req)
	if err != nil {
		return readPlan{}, err
	}
	keyset, keysetCols, err := readCursor(res.Entity, order, in)
	if err != nil {
		return readPlan{}, err
	}
	return readPlan{
		entityPlan: ep, projectionFields: req.Fields, order: order, rel: rel,
		full: andPreds(andPreds(pred, g.RowFilter), keyset), keysetCols: keysetCols, grant: g,
	}, nil
}

// checkReadUses refuses, before any query, a read of fields, or a comparison
// of them, the caller may not make: those of its filter and cursor, of the
// relationships it joins (locals) and of the expansions it asks for.
func checkReadUses(tc Context, res entity.Resolved, in readInput, readFields, locals []string) error {
	predicateFields, _ := splitPaths(res.Entity, append(filterFields(in.Filter), sortedKeys(in.Cursor)...))
	predicateFields = append(predicateFields, locals...) // a join on them compares their values
	// so does expansion's: a child carries the target's equal value
	predicateFields = append(predicateFields, expandJoinColumns(res.Entity, in.Expand)...)
	if err := (fieldUses{read: readFields, compared: predicateFields}).check(res); err != nil {
		return err
	}
	return checkExpansions(tc, res.Entity, in.Expand)
}

// checkExpansions refuses an expansion its configuration rules out: one
// joining on no field, on a target column the caller may not compare, or
// across datasources.
func checkExpansions(tc Context, parent entity.Entity, names []string) error {
	for _, name := range names {
		relation, _ := relationshipByName(parent, name)
		if len(relation.JoinOn) == 0 {
			return fmt.Errorf("%w: relationship %q joins on no field", ErrInvalidInput, name)
		}
		target, err := resolveDMLEntity(tc, relation.Target)
		if err != nil {
			return err
		}
		if err := (fieldUses{compared: relationJoin(relation).target}).check(target); err != nil {
			return err
		}
		if target.Entity.DataSource != parent.DataSource {
			return fmt.Errorf("%w: cross-datasource relationship %q", ErrInvalidInput, name)
		}
	}
	return nil
}

// relate plans what a request names across res's relationships (paths, and
// pred's exists groups): the joins, pred as the IR writes it, and the
// columns of res the relationships read.
func relate(ctx context.Context, tc Context, res entity.Resolved, pred relalg.Predicate, paths []string) (
	related, relalg.Predicate, []string, error,
) {
	rel, err := planJoins(ctx, tc, res, paths)
	if err != nil {
		return related{}, nil, nil, err
	}
	pred, locals, err := resolveExists(ctx, tc, res, rel.renamed(pred))
	if err != nil {
		return related{}, nil, nil, err
	}
	return rel, pred, append(rel.locals, locals...), nil
}

func orderFieldNames(terms []orderJSON) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = t.Field
	}
	return out
}

// orderRead plans in's orderBy and distinct on res and adds to req what they
// read: the ordered fields, and the ones nextCursor is built of, which the
// projection then carries too.
func orderRead(ctx context.Context, tc Context, res entity.Resolved, in readInput, req rbac.Request, rel related) (
	readOrder, rbac.Request, error,
) {
	if in.Distinct && (len(in.Fields) == 0 || len(in.Expand) > 0 || len(in.Cursor) > 0) {
		return readOrder{}, req, fmt.Errorf("%w: distinct needs fields, and takes no expand or cursor",
			ErrInvalidInput)
	}
	order, err := planReadOrder(res, in, rel)
	if err != nil {
		return readOrder{}, req, err
	}
	// What the request reads stays read (and authorized) whatever the key's
	// fate: the fields asked for are added to it, the key's only when the
	// caller may read them.
	own, _ := splitPaths(res.Entity, order.fields()) // related fields are authorized on the target
	for _, f := range own {
		if !slices.Contains(order.keyFields, f) {
			req.ReadFields = append(slices.Clone(req.ReadFields), f)
		}
	}
	if !readsKey(ctx, tc, req, order) {
		order = order.withoutKey()
	}
	req.ReadFields = append(slices.Clone(req.ReadFields), order.keyFields...)
	if order.pageable && len(in.Fields) > 0 {
		for _, f := range order.fields() {
			if !slices.ContainsFunc(req.Fields, func(x string) bool { return columnOf(res.Entity, x) == f }) {
				req.Fields = append(slices.Clone(req.Fields), f)
			}
		}
	}
	return order, req, nil
}

// readCursor resumes a read after in.Cursor: by its order when it names
// one, else on the entity's cursor key.
func readCursor(e entity.Entity, order readOrder, in readInput) (relalg.Predicate, []string, error) {
	if len(in.OrderBy) == 0 {
		return cursorPredicate(e, in.Cursor)
	}
	if len(in.Cursor) == 0 {
		return nil, nil, nil
	}
	p, err := orderCursor(order, in.Cursor)
	if err != nil {
		return nil, nil, err
	}
	p, err = coercePredicate(e, p) // as the key's cursor: numbers checked here, not by the database
	return p, nil, err
}

// cursorPredicate resumes keyset pagination after cursor on the entity's
// cursor key. A cursor that cannot be honored is rejected rather than
// ignored, which would return the first page again.
func cursorPredicate(e entity.Entity, cursor map[string]any) (relalg.Predicate, []string, error) {
	if len(cursor) == 0 {
		return nil, nil, nil
	}
	key := e.CursorKey()
	if key == nil {
		return nil, nil, fmt.Errorf(
			"%w: entity %q has no primary or unique key for cursor paging",
			ErrInvalidInput,
			e.Name,
		)
	}
	keyset, cols := keysetAfter(key, cursor)
	if len(cols) != len(cursor) {
		return nil, nil, fmt.Errorf("%w: cursor must name a leading part of the key (%s)",
			ErrInvalidInput, strings.Join(key, ", "))
	}
	keyset, err := coercePredicate(e, keyset)
	return keyset, cols, err
}

// authorizeRead authorizes a read; a distinct one compares the fields it
// returns (the database tells rows apart by them), so none may be masked.
func authorizeRead(ctx context.Context, tc Context, res entity.Resolved, in readInput, req rbac.Request) (
	grant, error,
) {
	g, err := authorizeRequest(ctx, tc, req)
	if err == nil && in.Distinct {
		err = fieldUses{compared: g.Fields}.check(res)
	}
	return g, err
}

func readFieldNames(res entity.Resolved, in readInput) ([]string, []string, error) {
	readFields := append(filterFields(in.Filter), in.Fields...)
	projectionFields := append([]string(nil), in.Fields...)
	for _, name := range in.Expand {
		if _, ok := relationshipByName(res.Entity, name); !ok {
			return nil, nil, fmt.Errorf("%w: unknown relationship %q", ErrInvalidInput, name)
		}
	}
	// The columns expansion joins on are read, and kept for it (st.keep),
	// as one list (expandJoinColumns).
	for _, local := range expandJoinColumns(res.Entity, in.Expand) {
		readFields = append(readFields, local)
		if len(in.Fields) > 0 && !slices.Contains(projectionFields, local) {
			projectionFields = append(projectionFields, local)
		}
	}
	return append(readFields, sortedKeys(in.Cursor)...), projectionFields, nil
}

func compileReadQuery(ctx context.Context, plan readPlan, in readInput) (statement, string, cache.Key, error) {
	tc, res := plan.tc, plan.res
	expr := buildReadExpression(res, in, plan)
	compiled, err := compile(tc, res.Entity, expr, identityKeys(tc, res.Entity, false))
	if err != nil {
		return statement{}, "", cache.Key{}, err
	}
	planTemplate := ""
	if _, off := tc.Feedback.(cost.NoopFeedbackStore); tc.Feedback != nil && !off { // the feedback's key only
		planTemplate = cost.Fingerprint(tc.DataSource, tc.Dialect.Name(), compiled)
	}
	sql, err := gateSQL(ctx, tc, compiled)
	if err != nil {
		err = withIndexHints(ctx, tc, res.Entity, err)
		return statement{}, "", cache.Key{}, err
	}
	key := cache.Key{Database: physicalDatabase(res.Entity)}
	if tc.Cache != nil { // the rest only finds an entry
		key.Relation, key.Datasource, key.Generation = cacheRelation(res.Entity), res.Entity.DatasourceName(),
			tc.Generation
		key.SQL = sql.SQL + "\x00expand=" + strings.Join(in.Expand, ",")
		key.Args, key.Scope = argsKey(sql.Args), scopeKey(tc.Role, tc.Subject)
	}
	return newStatement(tc, res, plan.grant, sql), planTemplate, key, nil
}

func buildReadExpression(res entity.Resolved, in readInput, plan readPlan) relalg.Expr {
	expr := plan.rel.wrap(relalg.Scan{Relation: res.Entity.Ref()})
	if plan.full != nil {
		expr = relalg.Select{Input: expr, Predicate: plan.full}
	}
	switch {
	case len(plan.order.terms) > 0:
		expr = relalg.Sort{Input: expr, OrderBy: plan.order.terms}
	case len(plan.keysetCols) > 0:
		order := make([]relalg.OrderTerm, len(plan.keysetCols))
		for i, c := range plan.keysetCols {
			order[i] = relalg.OrderTerm{Field: c, Dir: "asc"}
		}
		expr = relalg.Sort{Input: expr, OrderBy: order}
	}
	expr = relalg.Project{Input: expr, Items: projectItems(res.Entity, plan.grant.Fields)} // never empty

	if in.Distinct {
		expr = relalg.Distinct{Input: expr}
	}
	if in.Limit > 0 {
		expr = relalg.Limit{Input: expr, Count: in.Limit, Offset: in.Offset}
	}
	return expr
}

// cachesRead reports whether a read of in may be served from the cache and
// stored in it, both ways alike: not in a transaction (it sees its own
// writes), not expanded (expansion edits the rows), not following a write of
// the session. What it reads decides too (see runRead: no relationships).
func cachesRead(tc Context, in readInput) bool {
	return tc.Cache != nil && in.Transaction == "" && len(in.Expand) == 0 && !tc.followsWrite
}

// readCacheHit serves a read from the cache. The rows are the ones stored,
// shared by every call served from them: nothing after a read edits its rows
// (masking and trimming come before storeReadCache; a read that expands, which
// edits them, is not cached). TestCachedRowsStayAsStored holds it.
func readCacheHit(ctx context.Context, tc Context, in readInput, key cache.Key) (Result, error, bool) {
	if !cachesRead(tc, in) {
		return Result{}, nil, false
	}
	var cached []map[string]any
	var size int64
	ok := false
	if sized, isSized := tc.Cache.(cache.Sized[[]map[string]any]); isSized {
		cached, size, ok = sized.GetSized(ctx, key)
	} else {
		cached, ok = tc.Cache.Get(ctx, key)
	}
	if !ok {
		return Result{}, nil, false
	}
	if err := countCached(tc, cached, size); err != nil {
		return Result{}, err, true
	}
	returned, _ := tc.results.counted()
	return Result{Content: cached, ReturnedRows: returned}, nil, true
}

// trimExpandedJoinFields drops from out the columns read only to expand on
// (not asked for), worked out once for all the rows.
func trimExpandedJoinFields(parent entity.Entity, in readInput, out []map[string]any) {
	if len(in.Fields) == 0 {
		return
	}
	drop := slices.DeleteFunc(expandJoinColumns(parent, in.Expand), func(c string) bool {
		return slices.Contains(in.Fields, c)
	})
	for _, row := range out {
		for _, c := range drop {
			delete(row, c)
		}
	}
}

// countCached counts the rows of a read cached against the call's limits:
// by the size they were stored with (see storeReadCache), not encoded again,
// or one by one when it is not known.
func countCached(tc Context, rows []map[string]any, size int64) error {
	if size > 0 {
		return tc.results.addSized(int64(len(rows)), size)
	}
	for _, row := range rows {
		if err := tc.results.add(row); err != nil {
			return err
		}
	}
	return nil
}

func storeReadCache(ctx context.Context, tc Context, in readInput, key cache.Key, stamp uint64, out []map[string]any) {
	if !cachesRead(tc, in) {
		return
	}
	_, size := tc.results.counted() // a read cached has no expansion: its rows are the call's
	rowsAllowed := tc.CacheMaxEntryRows <= 0 || len(out) <= tc.CacheMaxEntryRows
	bytesAllowed := tc.CacheMaxEntryBytes <= 0 || size <= tc.CacheMaxEntryBytes
	if !rowsAllowed || !bytesAllowed {
		return
	}
	if sized, ok := tc.Cache.(cache.Sized[[]map[string]any]); ok {
		_ = sized.SetSized(ctx, key, out, stamp, size)
		return
	}
	_ = tc.Cache.Set(ctx, key, out, stamp)
}

// expandJoinColumns are the local columns the relationships expanded join
// on, whose values are kept before the rows are masked.
func expandJoinColumns(parent entity.Entity, names []string) []string {
	var columns []string
	for _, name := range names {
		relation, _ := relationshipByName(parent, name)
		for _, local := range slices.Sorted(maps.Keys(relation.JoinOn)) {
			if !slices.Contains(columns, local) {
				columns = append(columns, local)
			}
		}
	}
	return columns
}

func expandRows(ctx context.Context, tc Context, parent entity.Entity, parents queried, names []string) error {
	for _, name := range names {
		if err := expandOneRelationship(ctx, tc, parent, parents, name); err != nil {
			return err
		}
	}
	return nil
}

// expandOneRelationship expands parents by the relationship name, which
// checkExpansions admitted.
func expandOneRelationship(ctx context.Context, tc Context, parent entity.Entity, parents queried, name string) error {
	relation, _ := relationshipByName(parent, name)
	target, err := resolveDMLEntity(tc, relation.Target)
	if err != nil {
		return err
	}
	join := relationJoin(relation)
	join.localDecimal = decimalColumns(parent, join.local)
	join.targetDecimal = decimalColumns(target.Entity, join.target)
	targetTC, err := routeEntity(ctx, tc, target.Entity, entity.ActionRead)
	if err != nil {
		return err
	}
	tuples := uniqueJoinTuples(parents.kept, join.local, join.localDecimal)
	grouped := map[string][]map[string]any{}
	if len(tuples) > 0 {
		grouped, err = queryExpandedChildren(ctx, tc, targetTC, target, relation, join, tuples)
		if err != nil {
			return err
		}
	}
	attachExpandedChildren(parents, name, join, relation, grouped)
	return nil
}

// joinColumns pairs a relationship's local and target columns in a fixed
// order, with which of them hold decimals (see joinKeyOf).
type joinColumns struct {
	local, target               []string
	localDecimal, targetDecimal []bool
}

func relationJoin(relation entity.Relationship) joinColumns {
	var join joinColumns
	join.local = slices.Sorted(maps.Keys(relation.JoinOn))
	for _, local := range join.local {
		join.target = append(join.target, relation.JoinOn[local])
	}
	return join
}

// decimalColumns reports the columns of a numeric or decimal type, which
// drivers return as text.
func decimalColumns(e entity.Entity, columns []string) []bool {
	out := make([]bool, len(columns))
	for i, c := range columns {
		if a, ok := e.AttributeByName(c); ok {
			out[i] = isDecimalType(a.Domain.Type)
		}
	}
	return out
}

// joinKey is joinKeyOf with no decimal column.
func joinKey(values ...any) (key string, ok bool) { return joinKeyOf(values, nil) }

// joinKeyOf identifies a tuple of join values regardless of how the driver
// typed them: an int4 foreign key matches an int8 key, a float or the text
// of a decimal column the same number (12.50 matches 12.5), text may arrive
// as bytes; ok is false when a value is NULL, which matches nothing.
func joinKeyOf(values []any, decimal []bool) (key string, ok bool) {
	parts := make([]string, len(values))
	for i, v := range values {
		// Each part is quoted, so no value can contain the separator.
		switch x := v.(type) {
		case nil:
			return "", false
		case int64:
			parts[i] = "n:" + strconv.FormatInt(x, 10)
		case int, int8, int16, int32, uint, uint8, uint16, uint32, uint64:
			parts[i] = "n:" + fmt.Sprint(x)
		case float32:
			parts[i] = numberKey(strconv.FormatFloat(float64(x), 'g', -1, 32), x)
		case float64:
			parts[i] = numberKey(strconv.FormatFloat(x, 'g', -1, 64), x)
		case []byte:
			parts[i] = textKey(string(x), i < len(decimal) && decimal[i])
		case string:
			parts[i] = textKey(x, i < len(decimal) && decimal[i])
		default:
			parts[i] = fmt.Sprintf("%T:%q", x, fmt.Sprint(x))
		}
	}
	return strings.Join(parts, ","), true
}

// numberKey is the key of the number text spells, as an exact fraction in
// lowest terms; v's own key when text is not a finite number.
func numberKey(text string, v any) string {
	if key, ok := decimalKey(text); ok {
		return key
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return fmt.Sprintf("%T:%q", v, text)
	}
	return "n:" + r.RatString()
}

// decimalKey is numberKey of a plain decimal (an optional sign, digits, an
// optional point and digits) of at most 18 digits, worked out without a
// big.Rat, as big.Rat.RatString writes it: every row of an expansion keys
// its decimal join values.
func decimalKey(text string) (string, bool) {
	s, negative := text, false
	if len(s) > 0 && (s[0] == '-' || s[0] == '+') {
		s, negative = s[1:], s[0] == '-'
	}
	whole, frac, _ := strings.Cut(s, ".")
	digits := whole + frac
	if digits == "" || len(digits) > 18 || whole == "" && frac == "" {
		return "", false
	}
	var num int64
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", false
		}
		num = num*10 + int64(c-'0')
	}
	den := int64(1)
	for range frac {
		den *= 10
	}
	g := gcd(num, den)
	num, den = num/g, den/g
	if negative && num != 0 {
		num = -num
	}
	if den == 1 {
		return "n:" + strconv.FormatInt(num, 10), true
	}
	return "n:" + strconv.FormatInt(num, 10) + "/" + strconv.FormatInt(den, 10), true
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

func textKey(s string, decimal bool) string {
	if decimal {
		return numberKey(s, s)
	}
	return "s:" + strconv.Quote(s)
}

func rowJoinKey(row map[string]any, columns []string, decimal []bool) (string, bool) {
	values := make([]any, len(columns))
	for i, c := range columns {
		values[i] = row[c]
	}
	return joinKeyOf(values, decimal)
}

// uniqueJoinTuples lists the distinct non-NULL join tuples of rows.
func uniqueJoinTuples(rows []map[string]any, columns []string, decimal []bool) [][]any {
	tuples := make([][]any, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		key, ok := rowJoinKey(row, columns, decimal)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		tuple := make([]any, len(columns))
		for i, c := range columns {
			tuple[i] = row[c]
		}
		tuples = append(tuples, tuple)
	}
	return tuples
}

func queryExpandedChildren(
	ctx context.Context,
	tc Context,
	targetTC Context,
	target entity.Resolved,
	relation entity.Relationship,
	join joinColumns,
	tuples [][]any,
) (map[string][]map[string]any, error) {
	grouped := map[string][]map[string]any{}
	// A batch binds len(tuples)*len(columns) values: keep it within the IN
	// bound for a single column and the same number of values otherwise.
	batchSize := max(1, effectiveMaxIN(targetTC)/len(join.target))
	for start := 0; start < len(tuples); start += batchSize {
		stop := min(start+batchSize, len(tuples))
		st, cut, err := compileExpandBatch(ctx, tc, targetTC, target, relation, join, tuples[start:stop])
		if err != nil {
			return nil, err
		}
		// The batch reads one row past the row limit, and the executor refuses
		// it when that row arrives; a batch the gate cut shorter (at the row
		// limit, which no caller's exceeds) may have lost children at it.
		children, err := st.query(ctx)
		if err == nil && cut && int64(len(children.rows)) >= st.rowCap {
			err = budget.ErrExceeded
		}
		if err != nil {
			return nil, err
		}
		for i, child := range children.rows {
			if key, ok := rowJoinKey(children.kept[i], join.target, join.targetDecimal); ok {
				grouped[key] = append(grouped[key], child)
			}
		}
	}
	return grouped, nil
}

// joinPredicate selects the target rows matching tuples: an IN list for a
// single column, otherwise one conjunction of equalities per tuple.
func joinPredicate(columns []string, tuples [][]any) relalg.Predicate {
	if len(columns) == 1 {
		values := make([]any, len(tuples))
		for i, t := range tuples {
			values[i] = t[0]
		}
		return relalg.Condition{Field: columns[0], Op: relalg.OpIn, Value: values}
	}
	ors := make([]relalg.Predicate, len(tuples))
	for i, t := range tuples {
		ands := make([]relalg.Predicate, len(columns))
		for j, c := range columns {
			ands[j] = relalg.Condition{Field: c, Op: relalg.OpEq, Value: t[j]}
		}
		ors[i] = relalg.And{Preds: ands}
	}
	if len(ors) == 1 {
		return ors[0]
	}
	return relalg.Or{Preds: ors}
}

// compileExpandBatch plans the query of the children of tuples, bounded at
// one row past targetTC's row limit (rowCap): cut reports that the gate
// bounded it shorter, so that a batch reaching the limit may be incomplete.
func compileExpandBatch(
	ctx context.Context,
	tc Context,
	targetTC Context,
	target entity.Resolved,
	relation entity.Relationship,
	join joinColumns,
	tuples [][]any,
) (st statement, cut bool, err error) {
	predicate := joinPredicate(join.target, tuples)
	g, err := authorizeRequest(ctx, targetTC, rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: relation.Target,
		Action: entity.ActionRead, ReadFields: join.target, Predicate: predicate,
	})
	if err != nil {
		return statement{}, false, err
	}
	full := andPreds(predicate, g.RowFilter)
	scan := relalg.Scan{Relation: target.Entity.Ref()}
	expr := relalg.Expr(relalg.Select{Input: scan, Predicate: full})
	expr = relalg.Project{Input: expr, Items: relalg.ProjectFields(g.Fields)} // never empty (authorizeRequest)
	if targetTC.MaxRows > 0 {
		expr = relalg.Limit{Input: expr, Count: targetTC.MaxRows + 1}
	}
	compiled, err := compile(targetTC, target.Entity, expr, identityKeys(tc, target.Entity, false),
		codegen.WithMaxINCardinality(max(len(tuples), effectiveMaxIN(targetTC))))
	if err != nil {
		return statement{}, false, err
	}
	sql, err := gateSQL(ctx, targetTC, compiled)
	if err != nil {
		return statement{}, false, err
	}
	st = newStatement(targetTC, target, g, sql)
	st.keep, st.rowCap = join.target, targetTC.MaxRows
	return st, sql.SQL != compiled.SQL, nil
}

func attachExpandedChildren(
	parents queried,
	name string,
	join joinColumns,
	relation entity.Relationship,
	grouped map[string][]map[string]any,
) {
	for i, row := range parents.rows {
		var children []map[string]any
		if key, ok := rowJoinKey(parents.kept[i], join.local, join.localDecimal); ok {
			children = grouped[key]
		}
		switch {
		case relation.Single() && len(children) > 0:
			row[name] = children[0]
		case relation.Single():
			row[name] = nil
		case children == nil:
			row[name] = []map[string]any{}
		default:
			row[name] = children
		}
	}
}
