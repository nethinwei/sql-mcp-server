// Package store defines the database execution abstraction used by core tools.
//
// DB and Tx are minimal interfaces satisfied by *sql.DB / *sql.Tx via thin
// adapters in x/providers. Core packages depend on these interfaces so they
// can be tested with hand-written fakes and remain free of any database driver
// import. Iter turns a Rows stream into a range-friendly iterator for
// zero-materialization streaming of large result sets.
package store
