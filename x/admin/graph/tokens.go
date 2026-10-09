package graph

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
	out := McpToken{ID: t.ID, Name: t.Name, CreatedAt: t.CreatedAt}
	if !t.ExpiresAt.IsZero() {
		out.ExpiresAt = &t.ExpiresAt
	}
	if !t.LastUsedAt.IsZero() {
		out.LastUsedAt = &t.LastUsedAt
	}
	return out
}

func (r *Resolver) mcpTokens(ctx context.Context) ([]McpToken, error) {
	who, err := auth.Require(ctx, auth.PermRead)
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

func (r *Resolver) createMcpToken(ctx context.Context, in McpTokenInput) (*McpTokenSecret, error) {
	who, err := auth.Require(ctx, auth.PermRead)
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
	}
	token, err := config.NewUserToken()
	if err != nil {
		return nil, err
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	t := configstore.AdminToken{ID: hex.EncodeToString(id[:]), Username: who.Username, Name: name,
		TokenHash: config.TokenHash(token)}
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

func (r *Resolver) revokeMcpToken(ctx context.Context, id string) (bool, error) {
	who, err := auth.Require(ctx, auth.PermRead)
	if err != nil {
		return false, err
	}
	store, err := r.plans()
	if err != nil {
		return false, err
	}
	return true, store.DeleteAdminToken(ctx, who.Username, id)
}
