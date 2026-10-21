// Package catalog keeps what schema scans found, per database a datasource
// reads, so the console can search and plan over every scanned schema after
// a restart and on every replica. A scanned table is kept as a Table: the
// parts of an introspected entity that importing and planning read, in a
// stable JSON form independent of the runtime model.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/introspect"
)

// Source is what a datasource reads: its driver and read connection. What is
// kept belongs to a source, so a datasource that keeps its name but reads
// another database does not show what the old one had.
type Source struct {
	Datasource string
	Driver     string
	DSN        string
}

// ID identifies the source in a store without revealing its DSN.
func (s Source) ID() string {
	sum := sha256.Sum256([]byte(s.Datasource + "\x00" + s.Driver + "\x00" + s.DSN))
	return hex.EncodeToString(sum[:16])
}

// Token names the database the source reads, without its DSN; the console
// compares it to tell results of another database apart.
func (s Source) Token() string {
	sum := sha256.Sum256([]byte(s.Driver + "\x00" + s.DSN))
	return hex.EncodeToString(sum[:8])
}

// List is the schema list of a source.
type List struct {
	Schemas []string `json:"schemas"`
	// Current is where unqualified names resolve; empty when unknown.
	Current  string    `json:"current,omitempty"`
	ListedAt time.Time `json:"-"`
}

// Schema is what one scan found in a schema; "" names the default schema of
// a datasource that cannot list schemas.
type Schema struct {
	Name      string    `json:"-"`
	ScannedAt time.Time `json:"-"`
	Default   string    `json:"default,omitempty"`
	FoldCase  bool      `json:"foldCase,omitempty"`
	Tables    []Table   `json:"tables"`
}

// Version tells whether a kept schema changed since it was read.
type Version struct {
	Name      string
	ScannedAt time.Time
	Tables    int
}

// Table is one scanned table or view.
type Table struct {
	Schema      string       `json:"schema,omitempty"`
	Name        string       `json:"name"`
	View        bool         `json:"view,omitempty"`
	Derived     bool         `json:"derived,omitempty"`
	Description string       `json:"description,omitempty"`
	Columns     []Column     `json:"columns"`
	Keys        []Key        `json:"keys,omitempty"`
	Indexes     []Index      `json:"indexes,omitempty"`
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
	SideEffects bool         `json:"sideEffects,omitempty"`
}

// Column is a column of a table.
type Column struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	Nullable      bool   `json:"nullable,omitempty"`
	Default       bool   `json:"default,omitempty"`
	Generated     bool   `json:"generated,omitempty"`
	AutoIncrement bool   `json:"autoIncrement,omitempty"`
	Description   string `json:"description,omitempty"`
}

// Key is a primary or unique key; Reason says why one cannot identify a row.
type Key struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	Primary    bool     `json:"primary,omitempty"`
	Deferrable bool     `json:"deferrable,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// Index is an index in the database's own terms.
type Index struct {
	Name    string   `json:"name"`
	Method  string   `json:"method"`
	Parts   []string `json:"parts,omitempty"`
	Unique  bool     `json:"unique,omitempty"`
	Primary bool     `json:"primary,omitempty"`
	Where   string   `json:"where,omitempty"`
}

// ForeignKey references another table.
type ForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"refSchema,omitempty"`
	RefTable   string   `json:"refTable"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete,omitempty"`
	OnUpdate   string   `json:"onUpdate,omitempty"`
}

// FromEntity keeps what a scan found of a table.
func FromEntity(e entity.Entity) Table {
	t := Table{
		Schema: e.Schema, Name: e.Source, View: e.Kind == entity.KindView, Derived: e.Derived,
		Description: e.Description, SideEffects: e.SideEffects, Columns: make([]Column, 0, len(e.Attributes)),
	}
	if t.Name == "" {
		t.Name = e.Name
	}
	for _, a := range e.Attributes {
		t.Columns = append(t.Columns, Column{
			Name: a.Name, Type: a.Domain.Type, Nullable: a.Domain.Nullable, Default: a.Domain.Default,
			Generated: a.Domain.Generated, AutoIncrement: a.Domain.AutoIncrement, Description: a.Description,
		})
	}
	for _, k := range e.Keys {
		t.Keys = append(t.Keys, Key{
			Name: k.Name, Columns: k.Columns, Primary: k.Primary, Deferrable: k.Deferrable, Reason: k.Reason,
		})
	}
	for _, ix := range e.Indexes {
		t.Indexes = append(t.Indexes, Index{
			Name: ix.Name, Method: ix.Method, Parts: ix.Parts, Unique: ix.Unique, Primary: ix.Primary, Where: ix.Where,
		})
	}
	for _, fk := range e.ForeignKeys {
		t.ForeignKeys = append(t.ForeignKeys, ForeignKey{
			Name: fk.Name, Columns: fk.Columns, RefSchema: fk.RefSchema, RefTable: fk.RefRelation,
			RefColumns: fk.RefColumns, OnDelete: fk.OnDelete, OnUpdate: fk.OnUpdate,
		})
	}
	return t
}

// Entity is the introspected entity t was kept from, as far as kept.
func (t Table) Entity() entity.Entity {
	e := entity.Entity{
		Name: t.Name, Source: t.Name, Schema: t.Schema, Description: t.Description, Derived: t.Derived,
		SideEffects: t.SideEffects, Attributes: make([]entity.Attribute, 0, len(t.Columns)),
	}
	if t.View {
		e.Kind = entity.KindView
	}
	for _, c := range t.Columns {
		e.Attributes = append(e.Attributes, entity.Attribute{
			Name: c.Name, Description: c.Description, Domain: entity.Domain{
				Type: c.Type, Nullable: c.Nullable, Default: c.Default, Generated: c.Generated,
				AutoIncrement: c.AutoIncrement,
			},
		})
	}
	for _, k := range t.Keys {
		e.Keys = append(e.Keys, entity.Key{
			Name: k.Name, Columns: k.Columns, Primary: k.Primary, Deferrable: k.Deferrable, Reason: k.Reason,
		})
	}
	for _, ix := range t.Indexes {
		e.Indexes = append(e.Indexes, entity.Index{
			Name: ix.Name, Method: ix.Method, Parts: ix.Parts, Unique: ix.Unique, Primary: ix.Primary, Where: ix.Where,
		})
	}
	for _, fk := range t.ForeignKeys {
		e.ForeignKeys = append(e.ForeignKeys, entity.ForeignKey{
			Name: fk.Name, Columns: fk.Columns, RefSchema: fk.RefSchema, RefRelation: fk.RefTable,
			RefColumns: fk.RefColumns, OnDelete: fk.OnDelete, OnUpdate: fk.OnUpdate,
		})
	}
	return e
}

// FromCatalog splits a scan into the schemas it covered: schemas lists the
// schemas scanned, none for the default schema of a datasource that cannot
// list them, which holds all of the scan.
func FromCatalog(cat introspect.Catalog, schemas []string, at time.Time) []Schema {
	if len(schemas) == 0 {
		schemas = []string{""}
	}
	bySchema := map[string][]Table{}
	for _, t := range cat.Tables {
		bySchema[t.Schema] = append(bySchema[t.Schema], FromEntity(t))
	}
	out := make([]Schema, 0, len(schemas))
	for _, name := range schemas {
		tables := bySchema[name]
		if tables == nil && cat.FoldCase {
			// The server names it in its own case (lower, under MySQL's
			// lower_case_table_names): the schema is kept by the name asked.
			for schema, found := range bySchema {
				if strings.EqualFold(schema, name) {
					tables = append(tables, found...)
				}
			}
		}
		s := Schema{Name: name, ScannedAt: at, Default: cat.Default, FoldCase: cat.FoldCase, Tables: tables}
		if name == "" {
			s.Tables = make([]Table, 0, len(cat.Tables))
			for _, t := range cat.Tables {
				s.Tables = append(s.Tables, FromEntity(t))
			}
		}
		if s.Tables == nil {
			s.Tables = []Table{}
		}
		out = append(out, s)
	}
	return out
}

// Catalog puts kept schemas together as an indexed catalog.
func Catalog(schemas []Schema) introspect.Catalog {
	var cat introspect.Catalog
	for _, s := range schemas {
		cat.Default, cat.FoldCase = s.Default, s.FoldCase
		for _, t := range s.Tables {
			cat.Tables = append(cat.Tables, t.Entity())
		}
	}
	cat.Index()
	return cat
}
