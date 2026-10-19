package configstore

import (
	"slices"
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

func storeMigrations(text, longText string, onePublished []string) [][]string {
	return [][]string{revisionDDL(text, longText), adminDDL(), slices.Concat(catalogDDL(longText), templatesDDL(longText),
		tokensDDL(), pendingDDL(longText)), scopesAndItemsDDL(longText), workspacesDDL(longText), onePublished}
}

// Schema version 6 has the database hold at most one published revision:
// a partial unique index where there are partial indexes, otherwise a
// unique index on a generated column that is NULL but for the published one.
var (
	partialOnePublished = []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS smcp_revisions_one_published ON smcp_revisions (state)
			WHERE state = 'published'`,
	}
	generatedOnePublished = []string{
		`ALTER TABLE smcp_revisions ADD COLUMN published_flag INT
			AS (CASE WHEN state = 'published' THEN 1 ELSE NULL END) STORED`,
		`CREATE UNIQUE INDEX smcp_revisions_one_published ON smcp_revisions (published_flag)`,
	}
)

// catalogDDL is schema version 3, with planningDDL: the scanned catalog.
func catalogDDL(longText string) []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS smcp_catalog_lists (
			source_id      VARCHAR(64)  NOT NULL PRIMARY KEY,
			datasource     VARCHAR(255) NOT NULL,
			payload        ` + longText + ` NOT NULL,
			listed_us      BIGINT       NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS smcp_catalog_schemas (
			source_id   VARCHAR(64)  NOT NULL,
			schema_name VARCHAR(255) NOT NULL,
			datasource  VARCHAR(255) NOT NULL,
			table_count INT          NOT NULL,
			payload     ` + longText + ` NOT NULL,
			scanned_us  BIGINT       NOT NULL,
			PRIMARY KEY (source_id, schema_name)
		)`,
		`CREATE TABLE IF NOT EXISTS smcp_scan_batches (
			id           VARCHAR(32)  NOT NULL PRIMARY KEY,
			datasource   VARCHAR(255) NOT NULL,
			started_by   VARCHAR(64)  NOT NULL,
			owner        VARCHAR(64)  NOT NULL,
			started_us   BIGINT       NOT NULL,
			finished_us  BIGINT       NOT NULL,
			heartbeat_us BIGINT       NOT NULL,
			canceled     INT          NOT NULL,
			payload      ` + longText + ` NOT NULL
		)`,
	}
}

// templatesDDL, tokensDDL and pendingDDL are the rest of schema version 3:
// plan templates and their versions, administrators' MCP tokens and the
// state of pending work.
func templatesDDL(longText string) []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS smcp_plan_templates (
			name       VARCHAR(128) NOT NULL PRIMARY KEY,
			payload    ` + longText + ` NOT NULL,
			recurring  INT          NOT NULL,
			version    INT          NOT NULL,
			author     VARCHAR(255) NOT NULL,
			updated_us BIGINT       NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS smcp_plan_template_versions (
			name       VARCHAR(128) NOT NULL,
			version    INT          NOT NULL,
			payload    ` + longText + ` NOT NULL,
			recurring  INT          NOT NULL,
			author     VARCHAR(255) NOT NULL,
			updated_us BIGINT       NOT NULL,
			PRIMARY KEY (name, version)
		)`,
	}
}

func tokensDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS smcp_admin_tokens (
			id           VARCHAR(32)  NOT NULL PRIMARY KEY,
			username     VARCHAR(64)  NOT NULL,
			name         VARCHAR(64)  NOT NULL,
			token_hash   VARCHAR(80)  NOT NULL,
			created_us   BIGINT       NOT NULL,
			expires_us   BIGINT       NOT NULL,
			last_used_us BIGINT       NOT NULL
		)`,
		`CREATE UNIQUE INDEX smcp_admin_tokens_hash ON smcp_admin_tokens (token_hash)`,
	}
}

// scopesAndItemsDDL is schema version 4: the scopes of administrators'
// tokens (none: unlimited), the request keys of scan batches and their items
// as rows. The items kept in the payload of earlier batches move to rows in
// the same migration (migrateBatchItems); the payload is written empty since.
func scopesAndItemsDDL(longText string) []string {
	return []string{
		`ALTER TABLE smcp_admin_tokens ADD COLUMN scope VARCHAR(4000) NOT NULL DEFAULT ''`,
		`ALTER TABLE smcp_scan_batches ADD COLUMN request_key VARCHAR(64) NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS smcp_scan_batch_items (
			batch_id      VARCHAR(32)  NOT NULL,
			ordinal       INT          NOT NULL,
			schema_name   VARCHAR(255) NOT NULL,
			state         VARCHAR(16)  NOT NULL,
			error_text    ` + longText + ` NOT NULL,
			attempts      INT          NOT NULL,
			not_before_us BIGINT       NOT NULL,
			PRIMARY KEY (batch_id, schema_name)
		)`,
	}
}

// workspacesDDL is schema version 5: administrators' workspaces and the log
// of their writes.
func workspacesDDL(longText string) []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS smcp_workspaces (
			username   VARCHAR(64)  NOT NULL PRIMARY KEY,
			base_id    BIGINT       NOT NULL,
			items      ` + longText + ` NOT NULL,
			review     ` + longText + ` NOT NULL,
			version    BIGINT       NOT NULL,
			updated_us BIGINT       NOT NULL,
			updated_by VARCHAR(160) NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS smcp_workspace_log (
			username VARCHAR(64)  NOT NULL,
			version  BIGINT       NOT NULL,
			at_us    BIGINT       NOT NULL,
			author   VARCHAR(160) NOT NULL,
			op       VARCHAR(32)  NOT NULL,
			items    ` + longText + ` NOT NULL,
			note     ` + longText + ` NOT NULL,
			PRIMARY KEY (username, version)
		)`,
	}
}

func pendingDDL(longText string) []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS smcp_pending_states (
			item_key   VARCHAR(255) NOT NULL PRIMARY KEY,
			status     VARCHAR(16)  NOT NULL,
			owner      VARCHAR(64)  NOT NULL,
			reason     ` + longText + ` NOT NULL,
			until_us   BIGINT       NOT NULL,
			updated_by VARCHAR(64)  NOT NULL,
			updated_us BIGINT       NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS smcp_pending_log (
			item_key   VARCHAR(255) NOT NULL,
			updated_us BIGINT       NOT NULL,
			status     VARCHAR(16)  NOT NULL,
			owner      VARCHAR(64)  NOT NULL,
			reason     ` + longText + ` NOT NULL,
			until_us   BIGINT       NOT NULL,
			updated_by VARCHAR(64)  NOT NULL,
			PRIMARY KEY (item_key, updated_us)
		)`,
	}
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
		migrations: storeMigrations("TEXT", "TEXT", partialOnePublished),
	},
	"postgres": {
		driver:     "pgx",
		dollarArgs: true,
		prepareDSN: identity,
		migrations: storeMigrations("TEXT", "TEXT", partialOnePublished),
	},
	"mysql": {
		driver:     "mysql",
		prepareDSN: identity,
		migrations: storeMigrations("TEXT", "LONGTEXT", generatedOnePublished),
	},
	"oceanbase": {
		driver:     "mysql",
		prepareDSN: identity,
		migrations: storeMigrations("TEXT", "LONGTEXT", generatedOnePublished),
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
