package bootstrap

import (
	"log/slog"
	"reflect"
	"slices"

	"github.com/nethinwei/sql-mcp-server/core/config"
)

// A revocation must not wait for a whole configuration to apply: a published
// revision that disables a user may fail to apply for unrelated reasons (a
// database unreachable, its schema drifted, a restart needed). Once such a
// revision loads, the users it changes are held: they authenticate on
// neither the current configuration nor the new one until a reload applies
// one, and their sessions are dropped. What a revision only widens waits for
// it to apply.

// holdChanged holds the users of the current configuration that next changes
// (see changedUsers), in place of those an earlier revision held, and drops
// the sessions of the users newly held.
func (r *Runtime) holdChanged(next *config.Config) {
	r.reload.Lock() // a reload publishing meanwhile would release the hold
	defer r.reload.Unlock()
	app := r.Current()
	if app == nil {
		return
	}
	held := changedUsers(app, next)
	prev := r.held.Swap(&held)
	var principals []string
	for name := range held {
		if prev == nil || !(*prev)[name] {
			principals = append(principals, UserPrincipal(name))
		}
	}
	slices.Sort(principals)
	if len(principals) > 0 {
		slog.Warn("users held until the published revision applies", "principals", principals)
	}
	if fn := r.revoked.Load(); fn != nil && len(principals) > 0 {
		for _, session := range (*fn)(principals) {
			r.RollbackSession(session)
		}
	}
}

// heldUser reports whether name is held.
func (r *Runtime) heldUser(name string) bool {
	held := r.held.Load()
	return held != nil && (*held)[name]
}

// changedUsers lists the users servable by app that next removes, disables or
// gives other roles, subject, token, grants or permissions: a change that may
// narrow what they can do, held until it applies.
func changedUsers(app *App, next *config.Config) map[string]bool {
	users, tokens := userDirectory(next)
	held := map[string]bool{}
	for name, id := range app.Users {
		if to, ok := users[name]; !ok || !slices.Equal(id.Roles, to.Roles) || !sameValue(id.Subject, to.Subject) {
			held[name] = true
		}
		if cur := app.Config(); cur != nil && !sameUser(cur.Users[name], next.Users[name]) {
			held[name] = true
		}
	}
	for hash, name := range app.UserTokens {
		if tokens[hash] != name {
			held[name] = true
		}
	}
	return held
}

// sameUser reports whether a and b define the same user, but for the
// description.
func sameUser(a, b config.UserConfig) bool {
	return a.TokenHash == b.TokenHash && a.Disabled == b.Disabled && slices.Equal(a.Roles, b.Roles) &&
		slices.Equal(a.Permissions, b.Permissions) && sameValue(a.Subject, b.Subject) && sameValue(a.Grants, b.Grants)
}

// sameValue is reflect.DeepEqual but for an empty map or list, which is the
// same as none.
func sameValue[T any](a, b T) bool {
	if reflect.ValueOf(a).Len() == 0 && reflect.ValueOf(b).Len() == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
