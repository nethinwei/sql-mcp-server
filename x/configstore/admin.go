package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AdminAccount is an administrator of the admin API. Accounts are stored
// outside configuration revisions, so changes take effect immediately and a
// configuration publish never alters administrator access.
type AdminAccount struct {
	Username     string
	PasswordHash string
	Permissions  []string
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Admin account errors.
var (
	ErrAdminExists   = errors.New("configstore: admin account already exists")
	ErrAdminNotFound = errors.New("configstore: admin account not found")
)

const adminColumns = "username, password_hash, permissions, disabled, created_us, updated_us"

func scanAdmin(row scanner) (AdminAccount, error) {
	var a AdminAccount
	var permissions string
	var disabled int
	var created, updated int64
	if err := row.Scan(&a.Username, &a.PasswordHash, &permissions, &disabled, &created, &updated); err != nil {
		return AdminAccount{}, err
	}
	if permissions != "" {
		a.Permissions = strings.Split(permissions, ",")
	}
	a.Disabled = disabled != 0
	a.CreatedAt, a.UpdatedAt = fromMicros(created), fromMicros(updated)
	return a, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// CreateAdmin inserts a new account.
func (s *SQLStore) CreateAdmin(ctx context.Context, a AdminAccount) (AdminAccount, error) {
	now := s.timestamp()
	a.CreatedAt, a.UpdatedAt = now, now
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := s.queryRow(ctx, tx, "SELECT COUNT(*) FROM smcp_admin_accounts WHERE username = ?",
			a.Username).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: %q", ErrAdminExists, a.Username)
		}
		_, err := s.exec(ctx, tx, "INSERT INTO smcp_admin_accounts ("+adminColumns+") VALUES (?, ?, ?, ?, ?, ?)",
			a.Username, a.PasswordHash, strings.Join(a.Permissions, ","), boolInt(a.Disabled),
			toMicros(now), toMicros(now))
		return err
	})
	if err != nil {
		return AdminAccount{}, err
	}
	return a, nil
}

// GetAdmin returns one account.
func (s *SQLStore) GetAdmin(ctx context.Context, username string) (AdminAccount, error) {
	a, err := scanAdmin(s.queryRow(ctx, s.db, "SELECT "+adminColumns+" FROM smcp_admin_accounts WHERE username = ?",
		username))
	if errors.Is(err, sql.ErrNoRows) {
		return AdminAccount{}, fmt.Errorf("%w: %q", ErrAdminNotFound, username)
	}
	return a, err
}

// ListAdmins returns every account ordered by username.
func (s *SQLStore) ListAdmins(ctx context.Context) ([]AdminAccount, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+adminColumns+" FROM smcp_admin_accounts ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []AdminAccount
	for rows.Next() {
		a, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpdateAdmin replaces the password hash, permissions and disabled flag of an
// existing account.
func (s *SQLStore) UpdateAdmin(ctx context.Context, a AdminAccount) (AdminAccount, error) {
	now := s.timestamp()
	res, err := s.exec(ctx, s.db,
		"UPDATE smcp_admin_accounts SET password_hash = ?, permissions = ?, disabled = ?, updated_us = ? "+
			"WHERE username = ?",
		a.PasswordHash, strings.Join(a.Permissions, ","), boolInt(a.Disabled), toMicros(now), a.Username)
	if err != nil {
		return AdminAccount{}, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return AdminAccount{}, fmt.Errorf("%w: %q", ErrAdminNotFound, a.Username)
	}
	return s.GetAdmin(ctx, a.Username)
}
