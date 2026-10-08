package introspect

import "context"

// Privilege is what a connection's account may do: granted, denied, or
// unknown when the database does not say (such as privileges MySQL grants
// through roles). It informs administrators only; the database decides.
type Privilege uint8

const (
	PrivilegeUnknown Privilege = iota
	PrivilegeGranted
	PrivilegeDenied
)

func (p Privilege) String() string {
	switch p {
	case PrivilegeGranted:
		return "granted"
	case PrivilegeDenied:
		return "denied"
	}
	return "unknown"
}

// GrantedIf maps a known answer to a privilege.
func GrantedIf(granted bool) Privilege {
	if granted {
		return PrivilegeGranted
	}
	return PrivilegeDenied
}

// TablePrivileges are a connection's privileges on one table or view, with
// column-level privileges where the table-level one is not granted but some
// columns are.
type TablePrivileges struct {
	Select, Insert, Update, Delete Privilege
	// Columns lists, per action without a table-level grant, the columns
	// granted at column level ("select", "insert", "update").
	Columns map[string][]string
}

// PrivilegeInspector is implemented by introspectors that can report the
// privileges of their connection's account.
type PrivilegeInspector interface {
	// ReadOnly reports a server or session that refuses every write (a
	// replica, a hot standby, read_only).
	ReadOnly(ctx context.Context) (bool, error)
	TablePrivileges(ctx context.Context, schema, table string) (TablePrivileges, error)
	// ProcedurePrivilege reports EXECUTE on a procedure; unknown when its
	// name is overloaded.
	ProcedurePrivilege(ctx context.Context, schema, name string) (Privilege, error)
}
