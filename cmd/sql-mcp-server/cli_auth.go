package main

import (
	"log/slog"
	"sync"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/mcpserver"
)

// httpAuth is how cfg authenticates HTTP callers; configured users are
// resolved by users (the runtime's current snapshot).
func httpAuth(cfg *config.Config, users mcpserver.UserDirectory) mcpserver.HTTPAuth {
	a := cfg.Server.Auth
	auth := mcpserver.HTTPAuth{
		Token: a.Token, TrustProxyHeaders: a.TrustProxyHeaders, TrustedProxyCIDRs: a.TrustedProxyCIDRs,
		TLSCert: a.TLS.Cert, TLSKey: a.TLS.Key, ClientCA: a.TLS.ClientCA,
	}
	if len(cfg.Users) > 0 {
		auth.Users = users
	}
	return auth
}

// preparedAuths are the HTTP authentications reloads prepared, by the App
// each was prepared for. A reload prepares the authentication of its
// configuration before assembling it, reading its certificates, and fails if
// it cannot; the listener switches to it as the App is published, reading
// nothing, so a published configuration never serves with the previous
// authentication.
type preparedAuths struct {
	http  bool // nothing to prepare otherwise
	addr  string
	byApp sync.Map // *bootstrap.App → mcpserver.PreparedAuth
}

// prepare prepares the authentication next would serve HTTP callers with.
func (p *preparedAuths) prepare(next *config.Config) (*mcpserver.PreparedAuth, error) {
	if p == nil || !p.http {
		return nil, nil
	}
	auth, err := mcpserver.PrepareHTTPAuth(p.addr, httpAuth(next, checkedUsers{}))
	if err != nil {
		return nil, err
	}
	return &auth, nil
}

// keep records the authentication prepared for app.
func (p *preparedAuths) keep(app *bootstrap.App, auth *mcpserver.PreparedAuth) {
	if p != nil && auth != nil {
		p.byApp.Store(app, *auth)
	}
}

// follow switches the listener to the authentication of the current App and
// of each App a reload publishes (prepared by the reload, or now for one
// assembled otherwise, such as at startup), and forgets those prepared for
// Apps never published.
func (p *preparedAuths) follow(runtime *bootstrap.Runtime) func(func(mcpserver.PreparedAuth) error) {
	return func(apply func(mcpserver.PreparedAuth) error) {
		runtime.OnPublish(func(app *bootstrap.App) {
			stored, ok := p.byApp.LoadAndDelete(app)
			p.byApp.Clear()
			var prepared mcpserver.PreparedAuth
			switch {
			case ok:
				prepared = stored.(mcpserver.PreparedAuth)
			case app.Config() == nil:
				return
			default:
				auth, err := mcpserver.PrepareHTTPAuth(p.addr, httpAuth(app.Config(), checkedUsers{}))
				if err != nil {
					slog.Error("preparing the authentication failed; keeping the previous one", "error", err.Error())
					return
				}
				prepared = auth
			}
			if err := apply(prepared.WithUsers(runtime)); err != nil {
				slog.Error("switching to the new authentication failed; keeping the previous one", "error", err.Error())
			}
		})
	}
}

// checkedUsers stands for the users of a configuration being prepared: only
// whether there are any matters until it is published.
type checkedUsers struct{}

func (checkedUsers) UserByTokenHash(string) (bootstrap.UserIdentity, bool) {
	return bootstrap.UserIdentity{}, false
}

func (checkedUsers) UserByName(string) (bootstrap.UserIdentity, bool) {
	return bootstrap.UserIdentity{}, false
}
