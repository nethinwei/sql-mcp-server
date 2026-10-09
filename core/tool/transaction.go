package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

var (
	ErrTransactionNotFound = errors.New("tool: transaction not found")
	ErrTransactionScope    = errors.New("tool: transaction scope mismatch")
	ErrTransactionCapacity = errors.New("tool: transaction capacity reached")
	// ErrTransactionStale rejects statements in a transaction whose datasource
	// a reload has since pointed at another connection: the configuration they
	// would be planned with no longer describes the database it runs on.
	ErrTransactionStale = errors.New("tool: transaction datasource was reconfigured; roll it back and begin again")
)

type transactionHandle struct {
	tx         store.Tx
	connection store.TxBeginner // the connection begun on
	role       string
	subject    string
	session    string
	datasource string
	dirty      map[CacheTarget]struct{}
	expires    time.Time
	timer      *time.Timer
	cancel     context.CancelFunc
	mu         sync.Mutex
	closed     bool
}

// TransactionManager owns bounded, expiring explicit transaction handles.
// Tokens are unguessable and additionally bound to role, subject, and source.
type TransactionManager struct {
	mu      sync.Mutex
	handles map[string]*transactionHandle
	open    map[string]int // handles by scope
	ttl     time.Duration
	maxOpen int
	closed  bool
}

func NewTransactionManager(ttl time.Duration, maxOpen int) *TransactionManager {
	return &TransactionManager{
		handles: make(map[string]*transactionHandle), open: map[string]int{}, ttl: ttl, maxOpen: maxOpen,
	}
}

// UpdateLimits applies new limits: ttl to the transactions begun from now
// on, maxOpen to every later begin.
func (m *TransactionManager) UpdateLimits(ttl time.Duration, maxOpen int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ttl, m.maxOpen = ttl, maxOpen
}

func (m *TransactionManager) Begin(
	ctx context.Context,
	beginner store.TxBeginner,
	session, role string,
	subject map[string]any,
	datasource string,
	opts *store.TxOptions,
) (string, error) {
	scope := scopeKey(role, subject)
	if err := m.checkBeginCapacity(scope); err != nil {
		return "", err
	}
	ttl, _ := m.Configuration()
	lifetimeCtx, cancel := beginLifetimeContext(ctx, ttl)
	tx, err := beginTransaction(ctx, lifetimeCtx, cancel, beginner, opts)
	if err != nil {
		return "", err
	}
	token, err := newTransactionToken()
	if err != nil {
		cancel()
		_ = tx.Rollback()
		return "", err
	}
	handle := &transactionHandle{
		tx: tx, connection: beginner, session: session, role: role, subject: scopeKey("", subject),
		datasource: datasource, dirty: make(map[CacheTarget]struct{}), cancel: cancel,
	}
	if ttl > 0 {
		handle.expires = time.Now().Add(ttl)
	}
	if err := m.registerHandle(scope, token, handle); err != nil {
		return "", err
	}
	return token, nil
}

func (m *TransactionManager) checkBeginCapacity(scope string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrTransactionNotFound
	}
	if m.open[scope] >= m.maxOpen && m.maxOpen > 0 {
		return ErrTransactionCapacity
	}
	return nil
}

func beginLifetimeContext(ctx context.Context, ttl time.Duration) (context.Context, context.CancelFunc) {
	lifetimeCtx := context.WithoutCancel(ctx)
	if ttl > 0 {
		return context.WithTimeout(lifetimeCtx, ttl)
	}
	return context.WithCancel(lifetimeCtx)
}

func beginTransaction(
	ctx context.Context,
	lifetimeCtx context.Context,
	cancel context.CancelFunc,
	beginner store.TxBeginner,
	opts *store.TxOptions,
) (store.Tx, error) {
	stopRequestCancel := context.AfterFunc(ctx, cancel)
	tx, err := beginner.BeginTx(lifetimeCtx, opts)
	stopRequestCancel()
	if err != nil {
		cancel()
		if requestErr := ctx.Err(); requestErr != nil {
			return nil, requestErr
		}
		return nil, WrapDBError(err)
	}
	if err := ctx.Err(); err != nil {
		cancel()
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func newTransactionToken() (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(tokenBytes), nil
}

func (m *TransactionManager) registerHandle(scope, token string, handle *transactionHandle) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		handle.abort()
		return ErrTransactionNotFound
	}
	if m.maxOpen > 0 && m.open[scope] >= m.maxOpen {
		handle.abort()
		return ErrTransactionCapacity
	}
	m.handles[token] = handle
	m.open[scope]++
	if !handle.expires.IsZero() {
		handle.timer = time.AfterFunc(time.Until(handle.expires), func() { m.expire(token, handle) })
	}
	return nil
}

// removeLocked drops a handle; m.mu is held.
func (m *TransactionManager) removeLocked(token string, handle *transactionHandle) {
	delete(m.handles, token)
	scope := handle.role + handle.subject
	if m.open[scope]--; m.open[scope] <= 0 {
		delete(m.open, scope)
	}
}

func (m *TransactionManager) expire(token string, handle *transactionHandle) {
	m.mu.Lock()
	if m.handles[token] != handle {
		m.mu.Unlock()
		return
	}
	// Roll back before releasing m.mu: a lookup that misses the token then
	// observes a finished rollback.
	handle.abort()
	m.removeLocked(token, handle)
	m.mu.Unlock()
}

// abort rolls back the transaction unless it already ended and releases its
// lifetime context.
func (h *transactionHandle) abort() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		_ = h.tx.Rollback()
	}
	h.cancel()
}

// lookup finds the handle of token for the caller. Given connections, the
// current ones of datasource, the transaction must run on one of them.
func (m *TransactionManager) lookup(
	token, session, role string,
	subject map[string]any,
	datasource string,
	connections []store.TxBeginner,
) (*transactionHandle, error) {
	m.mu.Lock()
	handle := m.handles[token]
	m.mu.Unlock()
	if handle == nil {
		return nil, ErrTransactionNotFound
	}
	if handle.session != session || handle.role != role || handle.subject != scopeKey("", subject) ||
		datasource != "" && handle.datasource != datasource {
		return nil, ErrTransactionScope
	}
	if connections != nil && !slices.Contains(connections, handle.connection) {
		return nil, ErrTransactionStale
	}
	if !handle.expires.IsZero() && time.Now().After(handle.expires) {
		m.expire(token, handle)
		return nil, ErrTransactionNotFound
	}
	return handle, nil
}

func (m *TransactionManager) DB(
	token, session, role string,
	subject map[string]any,
	datasource string,
	connections []store.TxBeginner,
) (store.DB, error) {
	handle, err := m.lookup(token, session, role, subject, datasource, connections)
	if err != nil {
		return nil, err
	}
	return &transactionDB{handle: handle}, nil
}

// Validate verifies a token's transport and authorization identity without
// exposing its transaction or datasource.
func (m *TransactionManager) Validate(token, session, role string, subject map[string]any) error {
	_, err := m.lookup(token, session, role, subject, "", nil)
	return err
}

// Configuration returns the limits in force.
func (m *TransactionManager) Configuration() (time.Duration, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ttl, m.maxOpen
}

func (m *TransactionManager) finish(
	ctx context.Context,
	token, session, role string,
	subject map[string]any,
	commit bool,
) ([]CacheTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, err := m.lookup(token, session, role, subject, "", nil)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.handles[token] != handle {
		m.mu.Unlock()
		return nil, ErrTransactionNotFound
	}
	m.removeLocked(token, handle)
	if handle.timer != nil {
		handle.timer.Stop()
	}
	m.mu.Unlock()
	handle.mu.Lock()
	defer handle.mu.Unlock()
	defer handle.cancel()
	if handle.closed {
		return nil, ErrTransactionNotFound
	}
	handle.closed = true
	if err := ctx.Err(); err != nil {
		_ = handle.tx.Rollback()
		return nil, err
	}
	stopDeadline := context.AfterFunc(ctx, handle.cancel)
	defer stopDeadline()
	if commit {
		if err := handle.tx.Commit(); err != nil {
			_ = handle.tx.Rollback()
			return nil, WrapDBError(err)
		}
		targets := make([]CacheTarget, 0, len(handle.dirty))
		for target := range handle.dirty {
			targets = append(targets, target)
		}
		return targets, nil
	}
	return nil, WrapDBError(handle.tx.Rollback())
}

// Commit commits and returns the cache targets the transaction wrote. The list
// is returned only after a successful commit so callers can invalidate global
// read caches without rollback pollution.
func (m *TransactionManager) Commit(
	ctx context.Context,
	token, session, role string,
	subject map[string]any,
) ([]CacheTarget, error) {
	return m.finish(ctx, token, session, role, subject, true)
}

// Rollback rolls back the transaction.
func (m *TransactionManager) Rollback(ctx context.Context, token, session, role string, subject map[string]any) error {
	_, err := m.finish(ctx, token, session, role, subject, false)
	return err
}

// MarkDirty records the cache targets a write inside a transaction changed;
// connections are as for DB.
func (m *TransactionManager) MarkDirty(
	token, session, role string,
	subject map[string]any,
	datasource string,
	connections []store.TxBeginner,
	targets ...CacheTarget,
) error {
	handle, err := m.lookup(token, session, role, subject, datasource, connections)
	if err != nil {
		return err
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return ErrTransactionNotFound
	}
	for _, target := range targets {
		handle.dirty[target] = struct{}{}
	}
	return nil
}

// RollbackSession rolls back all handles owned by a disconnected non-empty
// transport session. Token-only transports use TTL and App.Close instead.
func (m *TransactionManager) RollbackSession(session string) {
	if session == "" {
		return
	}
	m.mu.Lock()
	handles := make([]*transactionHandle, 0)
	for token, handle := range m.handles {
		if handle.session == session {
			m.removeLocked(token, handle)
			if handle.timer != nil {
				handle.timer.Stop()
			}
			handles = append(handles, handle)
		}
	}
	m.mu.Unlock()
	for _, handle := range handles {
		handle.abort()
	}
}

func (m *TransactionManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	handles := m.handles
	m.handles, m.open = make(map[string]*transactionHandle), map[string]int{}
	m.mu.Unlock()
	for _, handle := range handles {
		if handle.timer != nil {
			handle.timer.Stop()
		}
		handle.abort()
	}
}

type transactionDB struct{ handle *transactionHandle }

func (d *transactionDB) QueryContext(ctx context.Context, query string, args ...any) (store.Rows, error) {
	d.handle.mu.Lock()
	if d.handle.closed {
		d.handle.mu.Unlock()
		return nil, ErrTransactionNotFound
	}
	rows, err := d.handle.tx.QueryContext(ctx, query, args...)
	if err != nil {
		d.handle.mu.Unlock()
		return nil, err
	}
	return &transactionRows{Rows: rows, unlock: d.handle.mu.Unlock}, nil
}

func (d *transactionDB) ExecContext(ctx context.Context, query string, args ...any) (store.Result, error) {
	d.handle.mu.Lock()
	defer d.handle.mu.Unlock()
	if d.handle.closed {
		return store.Result{}, ErrTransactionNotFound
	}
	return d.handle.tx.ExecContext(ctx, query, args...)
}

type transactionRows struct {
	store.Rows
	once   sync.Once
	unlock func()
	err    error
}

func (r *transactionRows) Close() error {
	r.once.Do(func() {
		r.err = r.Rows.Close()
		r.unlock()
	})
	return r.err
}

type BeginTransactionTool struct{}
type CommitTransactionTool struct{}
type RollbackTransactionTool struct{}

func (BeginTransactionTool) Info() Info {
	return Info{
		Name:        "begin_transaction",
		Action:      "transaction",
		Description: "Begin a bounded explicit transaction",
		InputSchema: schemaBeginTransaction,
	}
}
func (BeginTransactionTool) Enabled(f config.ToolFlags) bool { return f.BeginTransaction }
func (BeginTransactionTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, tc.TransactionBeginTimeout)
	defer cancel()
	in, err := parseBeginTransactionInput(input)
	if err != nil {
		return Result{}, err
	}
	beginner, datasource, err := resolveTxBeginner(tc, in.Datasource)
	if err != nil {
		return Result{}, err
	}
	readOnly, err := resolveTransactionReadOnly(ctx, tc, datasource, in.ReadOnly)
	if err != nil {
		return Result{}, err
	}
	// A read-only transaction runs on the read connection, unless the
	// session's reads follow its recent write.
	if source := tc.Sources[datasource]; readOnly && source.ReadTx != nil && !followsWrite(tc, source, datasource) {
		beginner = source.ReadTx
	}
	isolation, err := parseIsolation(in.Isolation)
	if err != nil {
		return Result{}, err
	}
	token, err := tc.Transactions.Begin(
		ctx,
		beginner,
		tc.Session,
		tc.Role,
		tc.Subject,
		datasource,
		&store.TxOptions{Isolation: isolation, ReadOnly: readOnly},
	)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"transaction": token, "datasource": datasource}}}, nil
}

type beginTransactionInput struct {
	Datasource string `json:"datasource"`
	Isolation  string `json:"isolation"`
	ReadOnly   *bool  `json:"readOnly"`
}

func parseBeginTransactionInput(input json.RawMessage) (beginTransactionInput, error) {
	var in beginTransactionInput
	if err := decodeInput(input, &in); err != nil {
		return beginTransactionInput{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if in.Datasource == "" {
		in.Datasource = "default"
	}
	return in, nil
}

// txConnections are the connections a transaction on datasource may run on
// under tc: its read-write and its read-only one; nil when tc routes none.
func txConnections(tc Context, datasource string) []store.TxBeginner {
	if tc.TxBeginners == nil {
		return nil
	}
	connections := []store.TxBeginner{tc.TxBeginners[datasource]}
	if source, ok := tc.Sources[datasource]; ok && source.ReadTx != nil {
		connections = append(connections, source.ReadTx)
	}
	return connections
}

func resolveTxBeginner(tc Context, datasource string) (store.TxBeginner, string, error) {
	beginner := tc.TxBeginners[datasource]
	if beginner == nil && datasource == "default" && len(tc.TxBeginners) == 1 {
		for name, candidate := range tc.TxBeginners {
			datasource, beginner = name, candidate
		}
	}
	if beginner == nil || tc.Transactions == nil {
		return nil, datasource, fmt.Errorf("%w: datasource %q", ErrInvalidInput, datasource)
	}
	return beginner, datasource, nil
}

func resolveTransactionReadOnly(
	ctx context.Context,
	tc Context,
	datasource string,
	requested *bool,
) (bool, error) {
	canRead, canWrite, err := transactionPermissions(ctx, tc, datasource)
	if err != nil {
		return false, err
	}
	if !canRead && !canWrite {
		return false, ErrUnauthorized
	}
	if requested != nil && !*requested {
		if !canWrite {
			return false, ErrUnauthorized
		}
		return false, nil
	}
	return true, nil
}

func transactionPermissions(ctx context.Context, tc Context, datasource string) (bool, bool, error) {
	if tc.Registry == nil || tc.Authorizer == nil {
		return false, false, nil
	}
	canRead, canWrite := false, false
	for _, e := range tc.Registry.Entities() {
		if e.DatasourceName() != datasource {
			continue
		}
		for _, action := range entity.ActionsFor(e.Kind) {
			dec, err := authorize(ctx, tc, rbac.Request{
				Role: tc.Role, Subject: tc.Subject, Entity: e.Name, Action: action,
			})
			if err != nil {
				return false, false, err
			}
			if !dec.Allowed {
				continue
			}
			switch action {
			case entity.ActionRead, entity.ActionAggregate:
				canRead = true
			default:
				canWrite = true
			}
		}
	}
	return canRead, canWrite, nil
}

func parseIsolation(value string) (store.IsolationLevel, error) {
	switch value {
	case "", "read_committed":
		return store.LevelReadCommitted, nil
	case "read_uncommitted":
		return store.LevelReadUncommitted, nil
	case "repeatable_read":
		return store.LevelRepeatableRead, nil
	case "serializable":
		return store.LevelSerializable, nil
	default:
		return 0, fmt.Errorf("%w: isolation %q", ErrInvalidInput, value)
	}
}

func (CommitTransactionTool) Info() Info {
	return Info{
		Name:        "commit_transaction",
		Action:      "transaction",
		Description: "Commit an explicit transaction",
		InputSchema: transactionTokenSchema,
	}
}
func (CommitTransactionTool) Enabled(f config.ToolFlags) bool { return f.CommitTransaction }
func (CommitTransactionTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, tc.TransactionCommitTimeout)
	defer cancel()
	token, err := transactionToken(input)
	if err != nil {
		return Result{}, err
	}
	if tc.Transactions == nil {
		return Result{}, ErrTransactionNotFound
	}
	targets, err := tc.Transactions.Commit(ctx, token, tc.Session, tc.Role, tc.Subject)
	if err != nil {
		return Result{}, err
	}
	_ = applyWrite(tc, writeTargets(tc), targets)
	return Result{Content: []map[string]any{{"committed": true}}}, nil
}

func (RollbackTransactionTool) Info() Info {
	return Info{
		Name:        "rollback_transaction",
		Action:      "transaction",
		Description: "Rollback an explicit transaction",
		InputSchema: transactionTokenSchema,
	}
}
func (RollbackTransactionTool) Enabled(f config.ToolFlags) bool { return f.RollbackTransaction }
func (RollbackTransactionTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	ctx, cancel := withTimeout(ctx, tc, tc.TransactionRollbackTimeout)
	defer cancel()
	token, err := transactionToken(input)
	if err != nil {
		return Result{}, err
	}
	if tc.Transactions == nil {
		return Result{}, ErrTransactionNotFound
	}
	if err := tc.Transactions.Rollback(ctx, token, tc.Session, tc.Role, tc.Subject); err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"rolledBack": true}}}, nil
}

func transactionToken(input json.RawMessage) (string, error) {
	var in struct {
		Transaction string `json:"transaction"`
	}
	if err := decodeInput(input, &in); err != nil || in.Transaction == "" {
		return "", fmt.Errorf("%w: transaction token is required", ErrInvalidInput)
	}
	return in.Transaction, nil
}
