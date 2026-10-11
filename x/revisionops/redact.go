package revisionops

import (
	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
)

// undecodable stands in for a payload that no longer decodes: its secrets
// cannot be found, so it is not shown.
const undecodable = "# this revision no longer decodes; its content is not shown\n"

// changedSecret marks a secret that differs from the other side's.
const changedSecret = " (changed)"

// redacted returns payload in the current encoding with its secrets (DSN
// passwords, the HTTP token) replaced; a secret that differs from other's
// (the payload compared with) is marked changed, so a diff shows that it
// changed but never its value.
func redacted(payload, other []byte) []byte {
	spec, err := loadable(payload)
	if err != nil {
		return []byte(undecodable)
	}
	var was *config.Spec
	if other != nil {
		if o, err := loadable(other); err == nil {
			was = o
		}
	}
	redactSpec(spec, was)
	out, err := configyaml.EncodeSpec(spec)
	if err != nil {
		return []byte(undecodable)
	}
	return out
}

// loadable is what payload says, when it loads.
func loadable(payload []byte) (*config.Spec, error) {
	spec, err := configyaml.DecodeSpec(payload)
	if err != nil {
		return nil, err
	}
	_, err = config.Resolve(spec)
	return spec, err
}

func redactSpec(spec, was *config.Spec) {
	if spec.Server != nil && spec.Server.Auth != nil && kit.Deref(spec.Server.Auth.Token) != "" {
		var then string
		if was != nil && was.Server != nil && was.Server.Auth != nil {
			then = kit.Deref(was.Server.Auth.Token)
		}
		spec.Server.Auth.Token = kit.Ptr("***" + marked(was != nil && then != kit.Deref(spec.Server.Auth.Token)))
	}
	if spec.Database != nil {
		var then *config.DatabaseSpec
		if was != nil {
			then = was.Database
		}
		redactDatabase(spec.Database, then)
	}
	for name, db := range spec.Databases {
		var then *config.DatabaseSpec
		if was != nil {
			if old, ok := was.Databases[name]; ok {
				then = &old
			}
		}
		redactDatabase(&db, then)
		spec.Databases[name] = db
	}
}

// redactDatabase redacts the DSNs of db, marking those that differ from
// then's (nil: nothing to compare with).
func redactDatabase(db, then *config.DatabaseSpec) {
	driver := kit.Deref(db.Driver)
	if dsn := kit.Deref(db.DSN); dsn != "" {
		changed := then != nil && kit.Deref(then.DSN) != dsn
		db.DSN = kit.Ptr(bootstrap.RedactDSN(driver, dsn) + marked(changed))
	}
	if db.Connections == nil {
		return
	}
	conns := make(config.Map[string, config.ConnectionSpec], len(db.Connections))
	for name, c := range db.Connections {
		changed := false
		if then != nil {
			old, ok := then.Connections[name]
			changed = ok && kit.Deref(old.DSN) != kit.Deref(c.DSN)
		}
		c.DSN = kit.Ptr(bootstrap.RedactDSN(driver, kit.Deref(c.DSN)) + marked(changed))
		conns[name] = c
	}
	db.Connections = conns
}

func marked(changed bool) string {
	if changed {
		return changedSecret
	}
	return ""
}
