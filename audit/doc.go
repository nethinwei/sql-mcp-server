// Package audit defines a best-effort, non-blocking audit sink. An Auditor
// records every tool call (who/when/what/result) for compliance. The async
// implementation drops events under backpressure and counts them rather than
// ever blocking or failing the main business flow (invariant I12).
package audit
