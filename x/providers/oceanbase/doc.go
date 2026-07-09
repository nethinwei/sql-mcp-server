// Package oceanbase adapts an OceanBase database (MySQL protocol) to the core
// interfaces. It reuses the mysql provider's Adapter and Introspector, and
// implements its own EXPLAIN parser (OceanBase's plan JSON differs from MySQL,
// and varies by version; the parser degrades to ScanUnknown on any surprise
// rather than panicking).
package oceanbase
