package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
	"github.com/nethinwei/sql-mcp-server/core/store"
)

type BeginTransactionTool struct{}
type CommitTransactionTool struct{}
type RollbackTransactionTool struct{}

func (BeginTransactionTool) Info() Info {
	return Info{
		Name:   "begin_transaction",
		Title:  "Begin transaction",
		Action: "transaction",
		Description: "Begin a transaction on one datasource and return its token. Pass the token as " +
			"transaction to the record tools, then commit_transaction or rollback_transaction it; it is " +
			"rolled back when its time limit passes or the session ends.",
		Object:      true,
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
	token, err := tc.Transactions.Begin(ctx, beginner, callerOf(tc), datasource,
		&store.TxOptions{Isolation: isolation, ReadOnly: readOnly})
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
	write := requested != nil && !*requested
	allowed, err := transactionAllowed(ctx, tc, datasource, write)
	if err != nil {
		return false, err
	}
	if !allowed {
		return false, ErrUnauthorized
	}
	return !write, nil
}

// transactionAllowed reports whether the caller may act on some entity of
// datasource: write it when write, else any action (a read-only transaction
// serves whoever may use the datasource). It stops at the first entity that
// says so: a datasource has thousands of entities.
func transactionAllowed(ctx context.Context, tc Context, datasource string, write bool) (bool, error) {
	if tc.Registry == nil || tc.Authorizer == nil {
		return false, nil
	}
	for e := range tc.Registry.All() {
		if e.DatasourceName() != datasource {
			continue
		}
		for _, action := range entity.ActionsFor(e.Kind) {
			if write && (action == entity.ActionRead || action == entity.ActionAggregate) {
				continue
			}
			dec, err := authorize(ctx, tc, rbac.Request{
				Role: tc.Role, Subject: tc.Subject, Entity: e.Name, Action: action,
			})
			if err != nil {
				return false, err
			}
			if dec.Allowed {
				return true, nil
			}
		}
	}
	return false, nil
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
		Title:       "Commit transaction",
		Action:      "transaction",
		Description: "Commit the transaction a begin_transaction token names, making its writes permanent.",
		Destructive: true, Object: true,
		InputSchema: transactionTokenSchema,
	}
}
func (CommitTransactionTool) Enabled(f config.ToolFlags) bool { return f.CommitTransaction }
func (CommitTransactionTool) endsTransaction()                {}
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
	targets, err := tc.Transactions.Commit(ctx, token, callerOf(tc))
	if errors.Is(err, ErrOutcomeUnknown) {
		_ = applyWrite(tc, writeTargets(tc), targets) // it may have committed
	}
	if err != nil {
		return Result{}, err
	}
	_ = applyWrite(tc, writeTargets(tc), targets)
	return Result{Content: []map[string]any{{"committed": true}}}, nil
}

func (RollbackTransactionTool) Info() Info {
	return Info{
		Name:        "rollback_transaction",
		Title:       "Roll back transaction",
		Action:      "transaction",
		Description: "Roll back the transaction a begin_transaction token names, discarding its writes.",
		Idempotent:  true, Object: true,
		InputSchema: transactionTokenSchema,
	}
}
func (RollbackTransactionTool) Enabled(f config.ToolFlags) bool { return f.RollbackTransaction }
func (RollbackTransactionTool) endsTransaction()                {}
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
	if err := tc.Transactions.Rollback(ctx, token, callerOf(tc)); err != nil {
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
