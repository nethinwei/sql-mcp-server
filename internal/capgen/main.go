// Command capgen generates the provider capability tables in
// docs/provider-compatibility.md from what each built-in provider claims
// (its dialect's capabilities and the limits it judges from a DSN and pooler).
// Run it with `go generate ./x/providers/all`; tests fail when the section is
// stale or a registered driver is missing.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/dialect"
	"github.com/nethinwei/sql-mcp-server/x/providerregistry"
	"github.com/nethinwei/sql-mcp-server/x/providers/mysql"
	"github.com/nethinwei/sql-mcp-server/x/providers/oceanbase"
	"github.com/nethinwei/sql-mcp-server/x/providers/postgres"
)

const (
	docPath     = "docs/provider-compatibility.md"
	beginMarker = "<!-- BEGIN GENERATED: provider-capabilities -->"
	endMarker   = "<!-- END GENERATED: provider-capabilities -->"
)

// scenario is a way to open a connection: a DSN and the options.
type scenario struct {
	name string
	dsn  string
	opts providerregistry.Options
}

// provider is a built-in provider and the connections its table shows.
type provider struct {
	driver, title string
	dialect       dialect.Dialect
	limits        func(string, providerregistry.Options) (dialect.Profile, error)
	scenarios     []scenario // the first is the default connection
}

// timed are the options of a connection under the default cost.queryTimeout.
var (
	timed  = providerregistry.Options{Timeout: 30 * time.Second}
	pooled = providerregistry.Options{Timeout: 30 * time.Second, Pooler: providerregistry.PoolerTransaction}
	// untimed is cost.queryTimeout 0: no statement timeout is set.
	untimed = providerregistry.Options{}
)

var providers = []provider{
	{"postgres", "PostgreSQL", postgres.Dialect{}, postgres.Limits, []scenario{
		{"默认", "postgres://u@h/db", timed},
		{"DSN 设置 `statement_timeout`", "postgres://u@h/db?statement_timeout=0", timed},
		{"`pooler: transaction`", "postgres://u@h/db", pooled},
		{"`cost.queryTimeout: 0`", "postgres://u@h/db", untimed},
	}},
	{"mysql", "MySQL", mysql.Dialect{}, mysql.Limits, []scenario{
		{"默认", "u:p@tcp(h)/db", timed},
		{"DSN 设置 `max_execution_time`", "u:p@tcp(h)/db?max_execution_time=0", timed},
		{"DSN 设置 `sql_safe_updates=0`", "u:p@tcp(h)/db?sql_safe_updates=0", timed},
		{"`pooler: transaction`", "u:p@tcp(h)/db", pooled},
		{"`cost.queryTimeout: 0`", "u:p@tcp(h)/db", untimed},
	}},
	{"oceanbase", "OceanBase", oceanbase.Dialect{}, oceanbase.Limits, []scenario{
		{"默认", "u:p@tcp(h)/db", timed},
		{"DSN 设置 `ob_query_timeout`", "u:p@tcp(h)/db?ob_query_timeout=0", timed},
		{"DSN 设置 `sql_safe_updates=0`", "u:p@tcp(h)/db?sql_safe_updates=0", timed},
		{"`pooler: transaction`", "u:p@tcp(h)/db", pooled},
		{"`cost.queryTimeout: 0`", "u:p@tcp(h)/db", untimed},
	}},
}

var evidenceLabels = map[dialect.Evidence]string{
	dialect.DBVerified: "真实数据库验证", dialect.CoreVerified: "核心层验证",
	dialect.Declared: "仅声明", dialect.External: "外部配置",
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if err := write(*root); err != nil {
		fmt.Fprintln(os.Stderr, "capgen:", err)
		os.Exit(1)
	}
}

func write(root string) error {
	doc, err := output(root)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, docPath), doc, 0o644)
}

// output is the document with its generated section replaced.
func output(root string) ([]byte, error) {
	current, err := os.ReadFile(filepath.Join(root, docPath))
	if err != nil {
		return nil, err
	}
	body, err := generate()
	if err != nil {
		return nil, err
	}
	doc := string(current)
	begin, end := strings.Index(doc, beginMarker), strings.Index(doc, endMarker)
	if begin < 0 || end < begin {
		return nil, errors.New(docPath + " lacks the generated-section markers")
	}
	return []byte(doc[:begin+len(beginMarker)] + "\n" + body + "\n" + doc[end:]), nil
}

// profile is what a connection of p opened as s claims.
func (p provider) profile(s scenario) (dialect.Profile, error) {
	limits, err := p.limits(s.dsn, s.opts)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", p.driver, s.name, err)
	}
	return p.dialect.Capabilities().With(limits), nil
}

func generate() (string, error) {
	defaults := make([]dialect.Profile, len(providers))
	for i, p := range providers {
		var err error
		if defaults[i], err = p.profile(p.scenarios[0]); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	b.WriteString("<!-- 本节由 internal/capgen 根据各 provider 的能力声明生成，勿手改；" +
		"`go generate ./x/providers/all` 重新生成。 -->\n\n")
	b.WriteString("默认连接（服务注入全部会话设置，不经 pooler）的保证强度与证据：\n\n")
	b.WriteString("| 能力 |")
	for _, p := range providers {
		b.WriteString(" " + p.title + " |")
	}
	b.WriteString("\n|---|" + strings.Repeat("---|", len(providers)) + "\n")
	for _, k := range dialect.Keys {
		b.WriteString("| `" + string(k) + "` |")
		for i := range providers {
			c := defaults[i].Claim(k)
			fmt.Fprintf(&b, " `%s`（%s） |", c.Strength, evidenceLabels[c.Evidence])
		}
		b.WriteString("\n")
	}
	b.WriteString("\n按连接判定的限制（默认连接，以及与默认不同的连接方式）：\n\n" +
		"| Provider | 连接方式 | 能力 | 强度 | 说明 |\n|---|---|---|---|---|\n")
	for i, p := range providers {
		for j, s := range p.scenarios {
			profile, err := p.profile(s)
			if err != nil {
				return "", err
			}
			for _, k := range []dialect.Key{dialect.TimeoutRead, dialect.TimeoutWrite, dialect.UnkeyedWrite} {
				c := profile.Claim(k)
				if d := defaults[i].Claim(k); j > 0 && c.Strength == d.Strength && c.Evidence == d.Evidence &&
					c.Reason.Text == d.Reason.Text {
					continue
				}
				fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` | %s |\n", p.title, s.name, k, c.Strength, c.Reason)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
