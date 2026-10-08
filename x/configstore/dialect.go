package configstore

import (
	"strconv"
	"strings"
)

// dialect captures the few differences between store backends: driver name,
// DSN preparation, placeholder style and column types in the DDL.
type dialect struct {
	driver     string
	dollarArgs bool
	prepareDSN func(string) string
	// migrations[v-1] holds the DDL that brings a store to schema version v.
	migrations [][]string
}

func storeMigrations(text, longText string) [][]string {
	return [][]string{revisionDDL(text, longText), adminDDL()}
}

// adminDDL is schema version 2: administrator accounts for the admin API.
func adminDDL() []string {
	return []string{`CREATE TABLE IF NOT EXISTS smcp_admin_accounts (
			username      VARCHAR(64)  NOT NULL PRIMARY KEY,
			password_hash VARCHAR(255) NOT NULL,
			permissions   VARCHAR(255) NOT NULL,
			disabled      INT          NOT NULL,
			created_us    BIGINT       NOT NULL,
			updated_us    BIGINT       NOT NULL
		)`}
}

func revisionDDL(text, longText string) []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS smcp_store_meta (
			meta_key   VARCHAR(64) NOT NULL PRIMARY KEY,
			meta_value BIGINT      NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS smcp_revisions (
			id           BIGINT       NOT NULL PRIMARY KEY,
			parent_id    BIGINT       NOT NULL,
			content_hash VARCHAR(80)  NOT NULL,
			payload      ` + longText + ` NOT NULL,
			state        VARCHAR(20)  NOT NULL,
			author       VARCHAR(255) NOT NULL,
			comment_text ` + text + ` NOT NULL,
			created_us   BIGINT       NOT NULL,
			published_us BIGINT       NOT NULL
		)`,
	}
}

var dialects = map[string]dialect{
	"sqlite": {
		driver:     "sqlite",
		prepareDSN: sqliteDSN,
		migrations: storeMigrations("TEXT", "TEXT"),
	},
	"postgres": {
		driver:     "pgx",
		dollarArgs: true,
		prepareDSN: identity,
		migrations: storeMigrations("TEXT", "TEXT"),
	},
	"mysql": {
		driver:     "mysql",
		prepareDSN: identity,
		migrations: storeMigrations("TEXT", "LONGTEXT"),
	},
	"oceanbase": {
		driver:     "mysql",
		prepareDSN: identity,
		migrations: storeMigrations("TEXT", "LONGTEXT"),
	},
}

func identity(dsn string) string { return dsn }

// sqliteDSN turns a path into a modernc DSN that waits for the write lock
// instead of failing with SQLITE_BUSY and uses WAL so readers (a polling
// server) do not block writers (the CLI).
func sqliteDSN(dsn string) string {
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
}

// rebind rewrites '?' placeholders to $n for PostgreSQL.
func (d dialect) rebind(query string) string {
	if !d.dollarArgs {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
