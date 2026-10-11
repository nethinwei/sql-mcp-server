package sqladapter

import (
	"database/sql"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
)

// TableKey names a relation across the schemas of one scan.
type TableKey struct{ Schema, Table string }

// Scanned is the metadata of the scanned schemas, by relation, as the
// queries of a scan fill it: each reads one kind of metadata of every
// relation at once, the way mysqldump reads a database, so the round trips
// do not grow with the relations. The Scan methods read the rows the
// providers' queries have in common.
type Scanned struct {
	Attrs       map[TableKey][]entity.Attribute
	Keys        map[TableKey][]entity.Key
	Indexes     map[TableKey][]entity.Index
	FKs         map[TableKey][]entity.ForeignKey
	Cascades    map[TableKey][]entity.Cascade
	SideEffects map[TableKey]bool
}

// NewScanned is an empty Scanned.
func NewScanned() *Scanned {
	return &Scanned{
		Attrs: map[TableKey][]entity.Attribute{}, Keys: map[TableKey][]entity.Key{},
		Indexes: map[TableKey][]entity.Index{}, FKs: map[TableKey][]entity.ForeignKey{},
		Cascades: map[TableKey][]entity.Cascade{}, SideEffects: map[TableKey]bool{},
	}
}

// Fill sets on e what the scan found of k: its columns, keys, indexes,
// foreign keys, cascades and write effects.
func (s *Scanned) Fill(k TableKey, e *entity.Entity) {
	e.Attributes, e.Keys, e.Indexes = s.Attrs[k], s.Keys[k], s.Indexes[k]
	e.ForeignKeys, e.Cascades, e.SideEffects = s.FKs[k], s.Cascades[k], s.SideEffects[k]
}

// ScanColumn reads a row of a columns query selecting, in order, the
// schema, relation, column, type, nullability (YES or NO), comment, and
// whether the database supplies a value, computes it and takes it from a
// sequence; the rows come in column order.
func (s *Scanned) ScanColumn(rows *sql.Rows) error {
	var k TableKey
	var name, dataType, nullable, comment string
	var domain entity.Domain
	if err := rows.Scan(&k.Schema, &k.Table, &name, &dataType, &nullable, &comment,
		&domain.Default, &domain.Generated, &domain.AutoIncrement); err != nil {
		return err
	}
	domain.Type, domain.Nullable = dataType, strings.EqualFold(nullable, "YES")
	s.Attrs[k] = append(s.Attrs[k], entity.Attribute{Name: name, Description: comment, Domain: domain})
	return nil
}

// ScanForeignKeyColumn reads a row of a foreign keys query selecting, in key
// order, the schema, relation, constraint, column, referenced schema,
// relation and column, and the delete and update rules, which actions maps
// to the entity's (a rule it leaves out only checks).
func (s *Scanned) ScanForeignKeyColumn(rows *sql.Rows, actions map[string]string) error {
	var k TableKey
	var name, col, refSchema, refTable, refCol, onDelete, onUpdate string
	if err := rows.Scan(&k.Schema, &k.Table, &name, &col, &refSchema, &refTable, &refCol,
		&onDelete, &onUpdate); err != nil {
		return err
	}
	fks := s.FKs[k]
	if n := len(fks); n == 0 || fks[n-1].Name != name {
		fks = append(fks, entity.ForeignKey{
			Name: name, RefSchema: refSchema, RefRelation: refTable,
			OnDelete: actions[onDelete], OnUpdate: actions[onUpdate],
		})
	}
	last := &fks[len(fks)-1]
	last.Columns = append(last.Columns, col)
	last.RefColumns = append(last.RefColumns, refCol)
	s.FKs[k] = fks
	return nil
}

// ScanSideEffect reads a row naming a relation (schema, relation) whose
// writes have effects the gateway cannot know: a trigger or a rule.
func (s *Scanned) ScanSideEffect(rows *sql.Rows) error {
	var k TableKey
	if err := rows.Scan(&k.Schema, &k.Table); err != nil {
		return err
	}
	s.SideEffects[k] = true
	return nil
}
