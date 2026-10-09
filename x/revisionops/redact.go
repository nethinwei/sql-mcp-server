package revisionops

import (
	"github.com/nethinwei/sql-mcp-server/core/config"
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
	cfg, err := configyaml.Decode(payload)
	if err != nil {
		return []byte(undecodable)
	}
	var was *config.Config
	if other != nil {
		if o, err := configyaml.Decode(other); err == nil {
			was = o
		}
	}
	redactConfig(cfg, was)
	out, err := configyaml.Encode(cfg)
	if err != nil {
		return []byte(undecodable)
	}
	return out
}

func redactConfig(cfg, was *config.Config) {
	if cfg.Server.Auth.Token != "" {
		changed := was != nil && was.Server.Auth.Token != cfg.Server.Auth.Token
		cfg.Server.Auth.Token = "***" + marked(changed)
	}
	var then *config.DatabaseConfig
	if was != nil {
		then = &was.Database
	}
	redactDatabase(&cfg.Database, then)
	for name, db := range cfg.Databases {
		then = nil
		if was != nil {
			if old, ok := was.Databases[name]; ok {
				then = &old
			}
		}
		redactDatabase(&db, then)
		cfg.Databases[name] = db
	}
}

// redactDatabase redacts the DSNs of db, marking those that differ from
// then's (nil: nothing to compare with).
func redactDatabase(db, then *config.DatabaseConfig) {
	if db.DSN != "" {
		changed := then != nil && then.DSN != db.DSN
		db.DSN = bootstrap.RedactDSN(db.Driver, db.DSN) + marked(changed)
	}
	if db.Connections == nil {
		return
	}
	conns := make(config.Connections, len(db.Connections))
	for name, c := range db.Connections {
		changed := false
		if then != nil {
			old, ok := then.Connections[name]
			changed = ok && old.DSN != c.DSN
		}
		c.DSN = bootstrap.RedactDSN(db.Driver, c.DSN) + marked(changed)
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
