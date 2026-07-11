// Package hook defines nil-safe lifecycle callbacks for observability. Hooks
// only observe; they cannot veto execution (authorization is a separate rbac
// concern). The core never imports a logging library; x/otel implements these
// callbacks to emit spans and metrics.
package hook
