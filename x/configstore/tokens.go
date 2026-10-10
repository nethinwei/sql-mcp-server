package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AdminToken is a named token of an administrator for the configuration MCP
// endpoint, one per client; the token itself is shown once and only its hash
// is stored.
type AdminToken struct {
	ID        string
	Username  string
	Name      string
	TokenHash string
	CreatedAt time.Time
	// ExpiresAt is when the token stops working; zero never.
	ExpiresAt time.Time
	// LastUsedAt is when the token last authenticated a request; zero never.
	LastUsedAt time.Time
	Scope      TokenScope
}

// TokenScope narrows what a token may do below its administrator's
// permissions.
type TokenScope struct {
	// Datasources limits the token to these datasources; nil does not.
	Datasources []string `json:"datasources,omitempty"`
	// ReadOnly marks a token created read only, which is no longer
	// offered: such a token is refused rather than given the administrator's
	// write permissions. New tokens never set it.
	ReadOnly bool `json:"readOnly,omitempty"`
}

// MaxAdminTokens bounds the tokens of one administrator.
const MaxAdminTokens = 20

// Admin token errors.
var (
	ErrTokenNotFound  = errors.New("configstore: admin token not found")
	ErrTooManyTokens  = fmt.Errorf("configstore: an administrator has at most %d tokens", MaxAdminTokens)
	ErrTokenNameTaken = errors.New("configstore: the administrator has a token with this name")
)

const tokenColumns = "id, username, name, token_hash, created_us, expires_us, last_used_us, scope"

func scanToken(row scanner) (AdminToken, error) {
	var t AdminToken
	var created, expires, used int64
	var scope string
	if err := row.Scan(&t.ID, &t.Username, &t.Name, &t.TokenHash, &created, &expires, &used, &scope); err != nil {
		return AdminToken{}, err
	}
	t.CreatedAt, t.ExpiresAt, t.LastUsedAt = fromMicros(created), fromMicros(expires), fromMicros(used)
	if scope != "" {
		if err := json.Unmarshal([]byte(scope), &t.Scope); err != nil {
			return AdminToken{}, fmt.Errorf("configstore: decode the scope of admin token %q: %w", t.ID, err)
		}
	}
	return t, nil
}

// AdminTokens returns an administrator's tokens, oldest first.
func (s *SQLStore) AdminTokens(ctx context.Context, username string) ([]AdminToken, error) {
	return s.adminTokens(ctx, s.db, username)
}

func (s *SQLStore) adminTokens(ctx context.Context, q execer, username string) ([]AdminToken, error) {
	rows, err := q.QueryContext(ctx, s.dialect.rebind(
		"SELECT "+tokenColumns+" FROM smcp_admin_tokens WHERE username = ? ORDER BY created_us, id"), username)
	if err != nil {
		return nil, fmt.Errorf("configstore: list admin tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []AdminToken{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AdminTokenByHash returns the token with a hash; false when none.
func (s *SQLStore) AdminTokenByHash(ctx context.Context, hash string) (AdminToken, bool, error) {
	t, err := scanToken(s.queryRow(ctx, s.db, "SELECT "+tokenColumns+" FROM smcp_admin_tokens WHERE token_hash = ?", hash))
	if errors.Is(err, sql.ErrNoRows) {
		return AdminToken{}, false, nil
	}
	if err != nil {
		return AdminToken{}, false, fmt.Errorf("configstore: read admin token: %w", err)
	}
	return t, true, nil
}

// CreateAdminToken stores a new token of t.Username, named uniquely among
// its tokens, and returns it with its creation time.
func (s *SQLStore) CreateAdminToken(ctx context.Context, t AdminToken) (AdminToken, error) {
	t.CreatedAt = s.timestamp()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		all, err := s.adminTokens(ctx, tx, t.Username)
		if err != nil {
			return err
		}
		if len(all) >= MaxAdminTokens {
			return ErrTooManyTokens
		}
		for _, other := range all {
			if other.Name == t.Name {
				return fmt.Errorf("%w: %q", ErrTokenNameTaken, t.Name)
			}
		}
		scope, err := json.Marshal(t.Scope)
		if err != nil {
			return err
		}
		_, err = s.exec(ctx, tx, "INSERT INTO smcp_admin_tokens ("+tokenColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			t.ID, t.Username, t.Name, t.TokenHash, toMicros(t.CreatedAt), toMicros(t.ExpiresAt), 0, string(scope))
		return err
	})
	if err != nil {
		return AdminToken{}, fmt.Errorf("configstore: create admin token: %w", err)
	}
	return t, nil
}

// DeleteAdminToken revokes one of an administrator's tokens.
func (s *SQLStore) DeleteAdminToken(ctx context.Context, username, id string) error {
	res, err := s.exec(ctx, s.db, "DELETE FROM smcp_admin_tokens WHERE username = ? AND id = ?", username, id)
	if err != nil {
		return fmt.Errorf("configstore: delete admin token: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("%w: %q", ErrTokenNotFound, id)
	}
	return nil
}

// TouchAdminToken records a use of a token.
func (s *SQLStore) TouchAdminToken(ctx context.Context, id string, at time.Time) error {
	_, err := s.exec(ctx, s.db, "UPDATE smcp_admin_tokens SET last_used_us = ? WHERE id = ?", toMicros(at), id)
	return err
}
