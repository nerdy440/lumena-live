// Package ctxkeys defines shared context keys used across all Lumena modules.
// Using a single package prevents the type-mismatch bug where two modules
// define `type contextKey string` with the same value — Go context keys are
// type-safe, so two identical strings in different types never match.
package ctxkeys

type key string

const AccountID key = "account_id"
