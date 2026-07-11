package tool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nethinwei/sql-mcp-server/config"
	"github.com/nethinwei/sql-mcp-server/entity"
	"github.com/nethinwei/sql-mcp-server/rbac"
	"github.com/nethinwei/sql-mcp-server/store"
)

var (
	ErrTransactionNotFound = errors.New("tool: transaction not found")
	ErrTransactionScope    = errors.New("tool: transaction scope mismatch")
	ErrTransactionCapacity = errors.New("tool: transaction capacity reached")
)

type transactionHandle struct {
	tx         store.Tx
	role       string
	subject    string
	session    string
	datasource string
	dirty      map[string]struct{}
	expires    time.Time
	timer      *time.Timer
	mu         sync.Mutex
	closed     bool
}

// TransactionManager owns bounded, expiring explicit transaction handles.
// Tokens are unguessable and additionally bound to role, subject, and source.
type TransactionManager struct {
	mu      sync.Mutex
	handles map[string]*transactionHandle
	ttl     time.Duration
	maxOpen int
	closed  bool
}

func NewTransactionManager(ttl time.Duration, maxOpen int) *TransactionManager {
	return &TransactionManager{handles: make(map[string]*transactionHandle), ttl: ttl, maxOpen: maxOpen}
}

func (m *TransactionManager) Begin(ctx context.Context, beginner store.TxBeginner, session, role string, subject map[string]any, datasource string, opts *store.TxOptions) (string, error) {
	scope := transactionScope(role, subject)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", ErrTransactionNotFound
	}
	if m.scopeCountLocked(scope) >= m.maxOpen && m.maxOpen > 0 {
		m.mu.Unlock()
		return "", ErrTransactionCapacity
	}
	m.mu.Unlock()
	tx, err := beginner.BeginTx(ctx, opts)
	if err != nil {
		return "", err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = tx.Rollback()
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	handle := &transactionHandle{
		tx: tx, session: session, role: role, subject: scopeKey("", subject), datasource: datasource,
		dirty: make(map[string]struct{}), expires: time.Now().Add(m.ttl),
	}
	m.mu.Lock()
	if m.closed || m.maxOpen > 0 && m.scopeCountLocked(scope) >= m.maxOpen {
		m.mu.Unlock()
		_ = tx.Rollback()
		if m.closed {
			return "", ErrTransactionNotFound
		}
		return "", ErrTransactionCapacity
	}
	m.handles[token] = handle
	if m.ttl > 0 {
		handle.timer = time.AfterFunc(m.ttl, func() { m.expire(token, handle) })
	}
	m.mu.Unlock()
	return token, nil
}

func transactionScope(role string, subject map[string]any) string {
	return scopeKey(role, subject)
}

func (m *TransactionManager) scopeCountLocked(scope string) int {
	count := 0
	for _, handle := range m.handles {
		if handle.role+handle.subject == scope {
			count++
		}
	}
	return count
}

func (m *TransactionManager) expire(token string, handle *transactionHandle) {
	m.mu.Lock()
	if m.handles[token] != handle {
		m.mu.Unlock()
		return
	}
	delete(m.handles, token)
	m.mu.Unlock()
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if !handle.closed {
		handle.closed = true
		_ = handle.tx.Rollback()
	}
}

func (m *TransactionManager) lookup(token, session, role string, subject map[string]any, datasource string) (*transactionHandle, error) {
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
	if m.ttl > 0 && time.Now().After(handle.expires) {
		m.expire(token, handle)
		return nil, ErrTransactionNotFound
	}
	return handle, nil
}

func (m *TransactionManager) DB(token, session, role string, subject map[string]any, datasource string) (store.DB, error) {
	handle, err := m.lookup(token, session, role, subject, datasource)
	if err != nil {
		return nil, err
	}
	return &transactionDB{handle: handle}, nil
}

// Validate verifies a token's transport and authorization identity without
// exposing its transaction or datasource.
func (m *TransactionManager) Validate(token, session, role string, subject map[string]any) error {
	_, err := m.lookup(token, session, role, subject, "")
	return err
}

// Configuration returns the immutable limits used by this manager.
func (m *TransactionManager) Configuration() (time.Duration, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ttl, m.maxOpen
}

func (m *TransactionManager) finish(token, session, role string, subject map[string]any, commit bool) ([]string, error) {
	handle, err := m.lookup(token, session, role, subject, "")
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.handles[token] != handle {
		m.mu.Unlock()
		return nil, ErrTransactionNotFound
	}
	delete(m.handles, token)
	if handle.timer != nil {
		handle.timer.Stop()
	}
	m.mu.Unlock()
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return nil, ErrTransactionNotFound
	}
	handle.closed = true
	if commit {
		if err := handle.tx.Commit(); err != nil {
			return nil, err
		}
		entities := make([]string, 0, len(handle.dirty))
		for entity := range handle.dirty {
			entities = append(entities, entity)
		}
		return entities, nil
	}
	return nil, handle.tx.Rollback()
}

func (m *TransactionManager) Commit(token, session, role string, subject map[string]any) error {
	_, err := m.finish(token, session, role, subject, true)
	return err
}

// CommitWithEntities commits and returns entities written by the transaction.
// The list is returned only after a successful commit so callers can invalidate
// global read caches without rollback pollution.
func (m *TransactionManager) CommitWithEntities(token, session, role string, subject map[string]any) ([]string, error) {
	return m.finish(token, session, role, subject, true)
}

func (m *TransactionManager) Rollback(token, session, role string, subject map[string]any) error {
	_, err := m.finish(token, session, role, subject, false)
	return err
}

// MarkDirty records an entity changed inside a transaction.
func (m *TransactionManager) MarkDirty(token, session, role string, subject map[string]any, datasource, entity string) error {
	handle, err := m.lookup(token, session, role, subject, datasource)
	if err != nil {
		return err
	}
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.closed {
		return ErrTransactionNotFound
	}
	handle.dirty[entity] = struct{}{}
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
			delete(m.handles, token)
			if handle.timer != nil {
				handle.timer.Stop()
			}
			handles = append(handles, handle)
		}
	}
	m.mu.Unlock()
	for _, handle := range handles {
		handle.mu.Lock()
		if !handle.closed {
			handle.closed = true
			_ = handle.tx.Rollback()
		}
		handle.mu.Unlock()
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
	m.handles = make(map[string]*transactionHandle)
	m.mu.Unlock()
	for _, handle := range handles {
		if handle.timer != nil {
			handle.timer.Stop()
		}
		handle.mu.Lock()
		if !handle.closed {
			handle.closed = true
			_ = handle.tx.Rollback()
		}
		handle.mu.Unlock()
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
	return Info{Name: "begin_transaction", Description: "Begin a bounded explicit transaction", InputSchema: schemaBeginTransaction}
}
func (BeginTransactionTool) Enabled(f config.ToolFlags) bool { return f.BeginTransaction }
func (BeginTransactionTool) Run(ctx context.Context, input json.RawMessage, tc Context) (Result, error) {
	var in struct {
		Datasource string `json:"datasource"`
		Isolation  string `json:"isolation"`
		ReadOnly   *bool  `json:"readOnly"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if in.Datasource == "" {
		in.Datasource = "default"
	}
	beginner := tc.TxBeginners[in.Datasource]
	if beginner == nil && in.Datasource == "default" && len(tc.TxBeginners) == 1 {
		for name, candidate := range tc.TxBeginners {
			in.Datasource, beginner = name, candidate
		}
	}
	if beginner == nil || tc.Transactions == nil {
		return Result{}, fmt.Errorf("%w: datasource %q", ErrInvalidInput, in.Datasource)
	}
	canRead, canWrite, err := transactionPermissions(ctx, tc, in.Datasource)
	if err != nil {
		return Result{}, err
	}
	if !canRead && !canWrite {
		return Result{}, ErrUnauthorized
	}
	readOnly := true
	if in.ReadOnly != nil && !*in.ReadOnly {
		if !canWrite {
			return Result{}, ErrUnauthorized
		}
		readOnly = false
	}
	isolation, err := parseIsolation(in.Isolation)
	if err != nil {
		return Result{}, err
	}
	token, err := tc.Transactions.Begin(ctx, beginner, tc.Session, tc.Role, tc.Subject, in.Datasource, &store.TxOptions{Isolation: isolation, ReadOnly: readOnly})
	if err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"transaction": token, "datasource": in.Datasource}}}, nil
}

func transactionPermissions(ctx context.Context, tc Context, datasource string) (bool, bool, error) {
	if tc.Registry == nil || tc.Authorizer == nil {
		return false, false, nil
	}
	canRead, canWrite := false, false
	for _, e := range tc.Registry.Entities() {
		source := e.DataSource
		if source == "" {
			source = "default"
		}
		if source != datasource {
			continue
		}
		for _, action := range []entity.Action{entity.ActionRead, entity.ActionAggregate, entity.ActionCreate, entity.ActionUpdate, entity.ActionDelete, entity.ActionExecute} {
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
	return Info{Name: "commit_transaction", Description: "Commit an explicit transaction", InputSchema: transactionTokenSchema}
}
func (CommitTransactionTool) Enabled(f config.ToolFlags) bool { return f.CommitTransaction }
func (CommitTransactionTool) Run(_ context.Context, input json.RawMessage, tc Context) (Result, error) {
	token, err := transactionToken(input)
	if err != nil {
		return Result{}, err
	}
	if tc.Transactions == nil {
		return Result{}, ErrTransactionNotFound
	}
	entities, err := tc.Transactions.CommitWithEntities(token, tc.Session, tc.Role, tc.Subject)
	if err != nil {
		return Result{}, err
	}
	if tc.Cache != nil {
		for _, entity := range entities {
			_ = tc.Cache.Invalidate(entity)
		}
	}
	return Result{Content: []map[string]any{{"committed": true}}}, nil
}

func (RollbackTransactionTool) Info() Info {
	return Info{Name: "rollback_transaction", Description: "Rollback an explicit transaction", InputSchema: transactionTokenSchema}
}
func (RollbackTransactionTool) Enabled(f config.ToolFlags) bool { return f.RollbackTransaction }
func (RollbackTransactionTool) Run(_ context.Context, input json.RawMessage, tc Context) (Result, error) {
	token, err := transactionToken(input)
	if err != nil {
		return Result{}, err
	}
	if tc.Transactions == nil {
		return Result{}, ErrTransactionNotFound
	}
	if err := tc.Transactions.Rollback(token, tc.Session, tc.Role, tc.Subject); err != nil {
		return Result{}, err
	}
	return Result{Content: []map[string]any{{"rolledBack": true}}}, nil
}

func transactionToken(input json.RawMessage) (string, error) {
	var in struct {
		Transaction string `json:"transaction"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.Transaction == "" {
		return "", fmt.Errorf("%w: transaction token is required", ErrInvalidInput)
	}
	return in.Transaction, nil
}
