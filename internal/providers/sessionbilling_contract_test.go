/**
 * Contract for providers that report their own session billing.
 *
 * A provider whose harness records the backend it billed answers through the
 * shared SessionBillingProvider contract instead of a provider-name branch in
 * shared code. Such a provider must not also claim subscription quota: its
 * evidence is session-local spend, never a plan window.
 */
package providers

import (
	"testing"

	"github.com/senna-lang/herdr-agent-usage/internal/provider"
)

func TestSessionBillingProviders_DoNotClaimSubscriptionQuota(t *testing.T) {
	seen := 0
	for _, r := range Registrations {
		if _, ok := r.Provider.(provider.SessionBillingProvider); !ok {
			continue
		}
		seen++
		if r.Has(CapOwnsSubscriptionQuota) || r.Has(CapRoutesToCollector) {
			t.Errorf("%s: reports its own session billing but also claims quota capabilities", r.Provider.AgentID())
		}
	}
	if seen == 0 {
		t.Skip("no session-billing providers registered")
	}
}
