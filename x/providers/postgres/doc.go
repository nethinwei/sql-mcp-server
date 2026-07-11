// Package postgres adapts a PostgreSQL database to the core interfaces:
// store.DB/Tx, dialect.Dialect, cost.Explainer, and
// introspect.Introspector. It is the only place that imports the pgx driver;
// core packages remain driver-free.
package postgres
