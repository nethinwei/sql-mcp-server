// Package workload generates the v4 realistic business workload fixture
// (docs/design/business-workload-model.md): four business modules rendered
// from one set of table specs into per-dialect DDL/seed SQL, entity
// configuration, and expected task results. Every value is a pure function
// of (seed, table, row index); regenerating with the same Config is
// byte-identical.
package workload

import (
	"fmt"
	"hash/fnv"
	"time"
)

// Config controls generation. The zero value is invalid; use DefaultConfig.
type Config struct {
	// Seed feeds every pseudo-random choice. Changing it produces a new,
	// equally deterministic dataset (and invalidates expected task results).
	Seed int64
	// Scale multiplies base row counts (1 = reference profile).
	Scale int
	// Anomalies toggles injected irregularities. The reference profile keeps
	// all of them on; disabling one removes that pattern deterministically.
	Anomalies Anomalies
}

// Anomalies are the injectable irregular data patterns.
type Anomalies struct {
	DuplicateCallbacks       bool // duplicate channel callbacks (dedup_of)
	OutOfOrderCallbacks      bool // received_at earlier than a prior event
	ReconciliationMismatches bool // statement items that do not reconcile
	FailedAttempts           bool // failed/retried payment attempts
	PartialRefunds           bool // partial (not full) refunds
	SoftDeletes              bool // logically deleted users/customers
	DuplicateGiftEvents      bool // duplicate gift events (dedup_of)
	UnpostedLedger           bool // payments succeeded but not yet posted
}

// DefaultConfig is the reference profile: seed 1, scale 1, all anomalies on.
func DefaultConfig() Config {
	return Config{Seed: 1, Scale: 1, Anomalies: Anomalies{
		DuplicateCallbacks:       true,
		OutOfOrderCallbacks:      true,
		ReconciliationMismatches: true,
		FailedAttempts:           true,
		PartialRefunds:           true,
		SoftDeletes:              true,
		DuplicateGiftEvents:      true,
		UnpostedLedger:           true,
	}}
}

// ColType is a portable logical column type; renderers map it per dialect.
type ColType int

// Logical column types shared by all dialect renderers.
const (
	ColInt ColType = iota
	ColBigInt
	ColText     // free text (never a key)
	ColCode     // short identifier text, usable as a key
	ColDate     // calendar date
	ColDateTime // wall-clock timestamp, stored as UTC without zone
)

// Column is one physical column with its agent-facing description. The
// description is the semantic contract surfaced through describe_entities;
// traps (units, grain, enums, time roles) live here on purpose.
type Column struct {
	Name        string
	Type        ColType
	Nullable    bool
	Description string
}

// Table is one physical table spec plus its generated rows.
type Table struct {
	Module      string // commerce-core | payment-orchestration | ledger-settlement | live-monetization
	Name        string // physical table name (wl_ prefix)
	Entity      string // exposed entity name
	Description string // agent-facing entity description
	PrimaryKey  []string
	Columns     []Column
	Rows        [][]any
}

// ColumnIndex returns the position of a column or panics: table specs and
// row builders live in this package, so a miss is a programming error.
func (t *Table) ColumnIndex(name string) int {
	for i, c := range t.Columns {
		if c.Name == name {
			return i
		}
	}
	panic(fmt.Sprintf("table %s has no column %s", t.Name, name))
}

// Dataset is the fully generated workload.
type Dataset struct {
	Config Config
	Tables []*Table
}

// Table returns a table by physical name or panics (programming error).
func (d *Dataset) Table(name string) *Table {
	for _, t := range d.Tables {
		if t.Name == name {
			return t
		}
	}
	panic(fmt.Sprintf("dataset has no table %s", name))
}

// Generate builds the whole dataset. Module builders run in dependency
// order and read earlier tables' rows, so relationships are consistent by
// construction.
func Generate(cfg Config) *Dataset {
	if cfg.Scale <= 0 {
		cfg.Scale = 1
	}
	d := &Dataset{Config: cfg}
	buildCommerce(d)
	buildPayments(d)
	buildLedger(d)
	buildLive(d)
	return d
}

const oneHour = time.Hour

// pick derives a deterministic pseudo-random uint64 from (seed, table,
// column, row). It is stateless so generation order cannot influence values.
func pick(seed int64, table, column string, n int) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d|%s|%s|%d", seed, table, column, n)
	return h.Sum64()
}

// day returns a date offset from the fixture epoch (2025-01-01).
func day(offset int) time.Time {
	return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, offset)
}

// at returns a timestamp on the given epoch day at hour/min/sec.
func at(dayOffset, hour, minute, second int) time.Time {
	d := day(dayOffset)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, minute, second, 0, time.UTC)
}

func fmtDate(t time.Time) string     { return t.Format("2006-01-02") }
func fmtDateTime(t time.Time) string { return t.Format("2006-01-02 15:04:05") }
