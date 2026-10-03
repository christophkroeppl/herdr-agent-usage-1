/**
 * UsageProvider for Hermes Agent.
 *
 * Hermes reports its session id through herdr's agent_session.kind=id, and
 * that id is the primary key of its own session store, so resolution is an
 * exact lookup with no cwd fallback: two panes in one directory are distinct
 * sessions. Hermes also records the backend it billed, so this adapter
 * implements the shared SessionBillingProvider contract and translates
 * Hermes's own billing-route vocabulary into the shared classification.
 */
package hermes

import (
	"strings"

	"github.com/senna-lang/herdr-agent-usage/internal/core"
	"github.com/senna-lang/herdr-agent-usage/internal/provider"
)

type usageProvider struct{}

// Provider is the Hermes Agent UsageProvider.
var Provider usageProvider

func (usageProvider) AgentID() string { return "hermes" }

func (usageProvider) ResolveUsage(input provider.UsageResolveInput) *core.ContextUsage {
	sessionID := provider.SessionID(input)
	if sessionID == nil {
		return nil
	}
	return ResolveUsageIn(ResolveHome(), *sessionID)
}

// ResolveSessionBilling reads only the session row; transcript and context
// cache state are irrelevant to billing facts.
func (usageProvider) ResolveSessionBilling(input provider.UsageResolveInput) (core.SessionBilling, bool) {
	sessionID := provider.SessionID(input)
	if sessionID == nil {
		return core.SessionBilling{}, false
	}
	db := openStateDB(ResolveHome())
	if db == nil {
		return core.SessionBilling{}, false
	}
	defer db.Close()
	row, ok := readSessionRow(db, *sessionID)
	if !ok {
		return core.SessionBilling{}, false
	}
	return *billingFromRow(row), true
}

// Hermes billing routes that cover a session's spend under a plan the agent
// is already paying for (agent/usage_pricing.py resolve_billing_route).
var subscriptionBillingModes = map[string]bool{"subscription_included": true}

// classifyBilling maps Hermes's persisted cost evidence to the shared class.
// actual/estimated mean token-priced usage. billing_mode contributes only the
// explicit subscription marker; API transport labels remain unclassified.
func classifyBilling(costStatus, mode string) core.BillingClass {
	if subscriptionBillingModes[strings.ToLower(strings.TrimSpace(mode))] {
		return core.BillingClassSubscription
	}
	switch strings.ToLower(strings.TrimSpace(costStatus)) {
	case "actual", "estimated":
		return core.BillingClassPayAsYouGo
	default:
		return core.BillingClassUnknown
	}
}

var (
	_ provider.UsageProvider          = usageProvider{}
	_ provider.SessionBillingProvider = usageProvider{}
)
