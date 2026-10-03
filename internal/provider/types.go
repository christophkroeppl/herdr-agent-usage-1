/**
 * Boundary types for per-agent usage providers.
 */
package provider

import "github.com/senna-lang/herdr-agent-usage/internal/core"

// AgentSession carries herdr's pane get agent_session verbatim.
// Interpreting kind/value (UUID vs path, etc.) is the provider's job.
type AgentSession struct {
	Kind  string
	Value string
}

// UsageResolveInput is the resolution input passed to a provider.
// The cwd is also passed for agents whose herdr integration does not
// report agent_session (currently Grok, etc.).
type UsageResolveInput struct {
	Session *AgentSession
	Cwd     *string
	// PaneID is herdr's pane id for the pane being resolved, when known.
	// Provider-neutral: an adapter whose upstream session identity can drift
	// from the agent_session herdr reported at launch resolves by pane instead,
	// which cwd cannot do because two panes may share one directory.
	PaneID *string
}

// UsageProvider resolves usage for a single agent.
// AgentID is matched against herdr's pane.agent (e.g. "claude", "codex", "grok").
type UsageProvider interface {
	AgentID() string
	ResolveUsage(input UsageResolveInput) *core.ContextUsage
}

// SessionBillingProvider is implemented by providers whose harness records
// what each session billed. The provider classifies its own vendor billing
// routes into the shared core.BillingClass, so shared layers never interpret
// a vendor string. An unknown class does not invalidate recorded backend,
// token, or cost facts. ok=false means the session has no billing evidence.
type SessionBillingProvider interface {
	UsageProvider
	ResolveSessionBilling(input UsageResolveInput) (billing core.SessionBilling, ok bool)
}

// FuncProvider is a function-backed UsageProvider.
type FuncProvider struct {
	ID   string
	Func func(UsageResolveInput) *core.ContextUsage
}

func (p FuncProvider) AgentID() string { return p.ID }

func (p FuncProvider) ResolveUsage(input UsageResolveInput) *core.ContextUsage {
	return p.Func(input)
}
