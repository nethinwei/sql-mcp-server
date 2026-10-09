package configapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

// maxTokenDays bounds how long a token may live.
const maxTokenDays = 3650

func toMcpToken(t configstore.AdminToken) McpToken {
	out := McpToken{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt, ReadOnly: t.Scope.ReadOnly,
		Datasources: t.Scope.Datasources}
	if !t.ExpiresAt.IsZero() {
		out.ExpiresAt = &t.ExpiresAt
	}
	if !t.LastUsedAt.IsZero() {
		out.LastUsedAt = &t.LastUsedAt
	}
	return out
}

// McpTokens lists the caller's tokens.
func (r *Service) McpTokens(ctx context.Context) ([]McpToken, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermRead)
	if err != nil {
		return nil, err
	}
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	all, err := store.AdminTokens(ctx, who.Username)
	if err != nil {
		return nil, err
	}
	out := make([]McpToken, 0, len(all))
	for _, t := range all {
		out = append(out, toMcpToken(t))
	}
	return out, nil
}

// CreateMcpToken creates a token of the caller for one client; the token
// is returned once.
func (r *Service) CreateMcpToken(ctx context.Context, in McpTokenInput) (*McpTokenSecret, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermRead)
	if err != nil {
		return nil, err
	}
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	days := derefInt(in.ExpiresInDays, 0)
	switch {
	case name == "" || utf8.RuneCountInString(name) > 64:
		return nil, errors.New("a token name has 1 to 64 characters")
	case days < 0 || days > maxTokenDays:
		return nil, errors.New("a token expires within 0 (never) to 3650 days")
	case in.Datasources != nil && len(in.Datasources) == 0:
		return nil, errors.New("name the datasources the token reaches, or none to reach every one")
	}
	token, err := config.NewUserToken()
	if err != nil {
		return nil, err
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	scope, err := r.tokenScope(ctx, in)
	if err != nil {
		return nil, err
	}
	t := configstore.AdminToken{ID: hex.EncodeToString(id[:]), Username: who.Username, Name: name,
		TokenHash: config.TokenHash(token), Scope: scope}
	if days > 0 {
		t.ExpiresAt = r.Scans.clock().Add(time.Duration(days) * 24 * time.Hour).UTC().Truncate(time.Microsecond)
	}
	saved, err := store.CreateAdminToken(ctx, t)
	if err != nil {
		return nil, err
	}
	info := toMcpToken(saved)
	return &McpTokenSecret{Token: token, Info: &info}, nil
}

// RevokeMcpToken revokes one of the caller's tokens.
func (r *Service) RevokeMcpToken(ctx context.Context, id string) (bool, error) {
	who, err := auth.RequireUnlimited(ctx, auth.PermRead)
	if err != nil {
		return false, err
	}
	store, err := r.plans()
	if err != nil {
		return false, err
	}
	return true, store.DeleteAdminToken(ctx, who.Username, id)
}

// tokenScope checks the datasources a token is limited to are configured.
func (r *Service) tokenScope(ctx context.Context, in McpTokenInput) (configstore.TokenScope, error) {
	scope := configstore.TokenScope{ReadOnly: derefBool(in.ReadOnly)}
	if in.Datasources == nil {
		return scope, nil
	}
	cfg, err := r.SelectConfig(ctx, nil, nil)
	if err != nil {
		return scope, err
	}
	for _, ds := range in.Datasources {
		if !hasDatasource(cfg, ds) {
			return scope, fmt.Errorf("unknown datasource %q", ds)
		}
		if !slices.Contains(scope.Datasources, ds) {
			scope.Datasources = append(scope.Datasources, ds)
		}
	}
	return scope, nil
}
