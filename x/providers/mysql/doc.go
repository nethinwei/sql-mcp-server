// Package mysql adapts a MySQL database to the core interfaces. The Adapter
// (store.DB/Tx/Canceler) is also reused by the oceanbase provider, since
// OceanBase speaks the MySQL wire protocol.
package mysql
