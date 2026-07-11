// Package mask defines field masking for sensitive data. A Masker applies a
// named rule (configured per attribute) to a value before it is returned,
// instead of excluding the column outright — so the agent sees shape, not
// secrets (e.g. email -> "a***@x.com"). Masking never raises: an unknown rule
// passes the value through unchanged so a misconfiguration cannot break reads.
package mask
