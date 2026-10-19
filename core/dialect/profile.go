package dialect

import "github.com/nethinwei/sql-mcp-server/core/reason"

// Strength is how firmly the server guarantees a capability.
type Strength string

const (
	// Unsupported: the server does not provide it.
	Unsupported Strength = "unsupported"
	// BestEffort: the server tries, but no hard guarantee rests on it.
	BestEffort Strength = "best_effort"
	// Enforced: an assembled, tested mechanism that does not silently degrade.
	Enforced Strength = "enforced"
)

// Reason is the strength as an explanation the console translates.
func (s Strength) Reason() reason.Reason {
	code := map[Strength]reason.Code{
		Enforced: reason.StrengthEnforced, BestEffort: reason.StrengthBestEffort, Unsupported: reason.StrengthUnsupported,
	}[s]
	return reason.New(code)
}

// Evidence is what backs a claim.
type Evidence string

const (
	// DBVerified: integration tests run it against the real database.
	DBVerified Evidence = "db_verified"
	// CoreVerified: shared-layer tests cover it; no test on this database.
	CoreVerified Evidence = "core_verified"
	// Declared: stated, not independently tested.
	Declared Evidence = "declared"
	// External: it rests on database or deployment configuration the server
	// does not manage.
	External Evidence = "external"
)

// Key names a capability.
type Key string

const (
	// Returning: INSERT ... RETURNING reads the inserted row back in one
	// statement (otherwise exec + the last insert id).
	Returning Key = "write.returning"
	// Explain: EXPLAIN yields a numeric estimate the cost gate can score.
	Explain Key = "cost.explain"
	// ExplainAccurate: the estimates are trustworthy enough to gate on
	// without explain fail-closed.
	ExplainAccurate Key = "cost.explain.accurate"
	// ExplainAnalyze: EXPLAIN ANALYZE sampling for estimate feedback.
	ExplainAnalyze Key = "cost.explain.analyze"
	// TimeoutRead: a read statement stops at cost.queryTimeout on the server.
	TimeoutRead Key = "limit.timeout.read"
	// TimeoutWrite: a writing statement stops at cost.queryTimeout on the
	// server.
	TimeoutWrite Key = "limit.timeout.write"
	// UnkeyedWrite: the database refuses UPDATE/DELETE without a key, as a
	// backstop behind the gate's WriteGuard.
	UnkeyedWrite Key = "limit.unkeyedWrite"
)

// Keys lists every capability in report order.
var Keys = []Key{Returning, Explain, ExplainAccurate, ExplainAnalyze, TimeoutRead, TimeoutWrite, UnkeyedWrite}

// Claim is what a dialect or connection guarantees for one capability.
type Claim struct {
	Strength Strength
	Evidence Evidence
	// Reason says how it is provided or why it is degraded.
	Reason reason.Reason
}

// Profile holds the claims of a dialect or connection. A missing key is
// unsupported.
type Profile map[Key]Claim

// Claim returns the claim for k; a missing key is unsupported.
func (p Profile) Claim(k Key) Claim {
	if c, ok := p[k]; ok {
		return c
	}
	return Claim{Strength: Unsupported, Evidence: Declared}
}

// Has reports whether k is provided at all (best effort or enforced).
func (p Profile) Has(k Key) bool { return p.Claim(k).Strength != Unsupported }

// Enforced reports whether k is enforced.
func (p Profile) Enforced(k Key) bool { return p.Claim(k).Strength == Enforced }

// With returns a copy of p with others' claims added or replacing p's.
func (p Profile) With(others Profile) Profile {
	out := make(Profile, len(p)+len(others))
	for k, c := range p {
		out[k] = c
	}
	for k, c := range others {
		out[k] = c
	}
	return out
}
