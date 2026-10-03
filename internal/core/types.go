/**
 * Shared types for the core layer. Provider-specific usage shapes do not belong here.
 */
package core

// BillingClass is how a session's spend is accounted for. Providers classify
// their own vendor-specific billing routes into these shared values, so no
// shared layer ever interprets a vendor billing string.
type BillingClass int

const (
	// BillingClassUnknown means the provider has no evidence either way.
	BillingClassUnknown BillingClass = iota
	// BillingClassSubscription means the session's spend is covered by a
	// plan, so quota windows (not a burn total) are the meaningful display.
	BillingClassSubscription
	// BillingClassPayAsYouGo means the session is billed per token, so its
	// own token and cost totals are the meaningful display.
	BillingClassPayAsYouGo
)

// SessionBilling is one session's own billing facts, reported by providers
// whose harness records what it billed. Backend is a display label for the
// billed endpoint; Tokens and CostUSD are session-cumulative and remain valid
// when Class is unknown.
type SessionBilling struct {
	Class   BillingClass
	Backend string
	Tokens  int
	CostUSD float64
}

// ContextUsage is the minimum usage information required for display.
// Token aggregation and model-window resolution must already be done by each
// provider; this type only carries the final result.
type ContextUsage struct {
	// ContextTokens is the current context-occupying token count (already aggregated).
	ContextTokens int
	// ContextUnavailable distinguishes an unresolved measurement from a measured
	// zero while allowing providers to keep publishing cache and billing facts.
	ContextUnavailable bool
	// WindowTokens is the context window size if known. When nil, only the absolute token count is shown.
	WindowTokens *int
	// Compacted marks a post-compaction estimate (no real usage row yet):
	// the display shows a "compacted" label instead of a measured size.
	Compacted bool
	// Cache is the latest completed turn, including recorded TTL when known.
	Cache *CacheUsage
	// SessionCache aggregates prompt-cache counters for the current transcript
	// segment. Sidebar hit rate prefers this; recorded TTL still comes from Cache.
	SessionCache *CacheUsage
	// Billing carries the session's own billing facts when the provider
	// records them, and is nil when it does not.
	Billing *SessionBilling
}
