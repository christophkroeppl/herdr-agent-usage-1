package hermes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/senna-lang/herdr-agent-usage/internal/core"
	"github.com/senna-lang/herdr-agent-usage/internal/provider"
	_ "modernc.org/sqlite"
)

func openFixture(t *testing.T, home string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (
 id TEXT PRIMARY KEY, cwd TEXT, model TEXT, model_config TEXT,
 input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER,
 cache_write_tokens INTEGER, reasoning_tokens INTEGER, billing_provider TEXT,
 billing_base_url TEXT, billing_mode TEXT, cost_status TEXT, estimated_cost_usd REAL,
 actual_cost_usd REAL);
CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT,
 content TEXT, api_content TEXT, tool_call_id TEXT, tool_calls TEXT, active INTEGER);`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func text(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func sessionTokens(t *testing.T, usage *core.ContextUsage) int {
	t.Helper()
	if usage == nil || usage.Billing == nil {
		t.Fatalf("no billing facts on %#v", usage)
	}
	return usage.Billing.Tokens
}

func anchorConfig(t *testing.T, messages []message, prompt, completion int) string {
	t.Helper()
	a := map[string]any{
		"prompt_tokens": prompt, "completion_tokens": completion,
		"base_count": len(messages), "base_last_role": messages[len(messages)-1].Role,
		"base_last_fp":   messageFingerprint(messages[len(messages)-1]),
		"base_prefix_fp": prefixFingerprint(messages),
	}
	raw, err := json.Marshal(map[string]any{"_usage_anchor": a})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestResolveUsageInRequiresExactSessionID(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	_, err := db.Exec(`INSERT INTO sessions (id,cwd,model,model_config,input_tokens,output_tokens) VALUES
 ('wanted','/same','model-a','{}',111,22), ('other','/same','model-a','{}',999,99)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	got := ResolveUsageIn(home, "wanted")
	if sessionTokens(t, got) != 133 {
		t.Fatalf("exact session usage = %#v, want 133", got)
	}
	if got := ResolveUsageIn(home, "missing"); got != nil {
		t.Fatalf("missing = %#v, want nil", got)
	}
}

func TestResolveUsageInReadsWALWithoutWriting(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens) VALUES ('wal','m','{}',7)`); err != nil {
		t.Fatal(err)
	}
	if got := sessionTokens(t, ResolveUsageIn(home, "wal")); got != 7 {
		t.Fatalf("WAL usage = %d, want 7", got)
	}
	db.Close()
}

func TestResolveUsageInToleratesOptionalColumns(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, model TEXT, model_config TEXT, input_tokens INTEGER, output_tokens INTEGER);
CREATE TABLE messages (id INTEGER PRIMARY KEY, session_id TEXT, role TEXT, content TEXT);
INSERT INTO sessions VALUES ('old','m','{}',8,3)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if got := sessionTokens(t, ResolveUsageIn(home, "old")); got != 11 {
		t.Fatalf("old schema tokens = %d, want 11", got)
	}
}

func TestAnchoredContextUsesOnlyActiveMatchingPrefixAndDelta(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	base := []message{{Role: "user", Content: text("hello")}, {Role: "assistant", Content: text("answer")}}
	cfg := anchorConfig(t, base, 100, 5)
	_, err := db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens,output_tokens) VALUES ('s','m',?,900,100)`, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO messages (session_id,role,content,active) VALUES
 ('s','user','hello',1),('s','assistant','answer',1),('s','assistant','priced reply',1),('s','user','small delta',1),('s','user','inactive rewrite',0)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	got := ResolveUsageIn(home, "s")
	if got == nil || got.ContextTokens <= 105 || got.ContextTokens >= 130 {
		t.Fatalf("anchored context = %#v", got)
	}
	if got.ContextTokens == sessionTokens(t, got) {
		t.Fatal("context must not be the lifetime total")
	}
}

func TestStaleAnchorDoesNotUseLifetimeAsContext(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	base := []message{{Role: "user", Content: text("original")}}
	cfg := anchorConfig(t, base, 100, 5)
	if _, err := db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens,output_tokens) VALUES ('s','m',?,900,100)`, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages (session_id,role,content,active) VALUES ('s','user','changed',1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	got := ResolveUsageIn(home, "s")
	if got == nil || !got.ContextUnavailable || got.ContextTokens != 0 || sessionTokens(t, got) != 1000 {
		t.Fatalf("stale anchor = %#v", got)
	}
	if status := core.FormatUsageStatus(*got, core.FormatUsageOptions{}); status != "" {
		t.Fatalf("stale anchor rendered context status %q, want empty", status)
	}
}

// Hermes's fingerprint includes any field that is not None, so a persisted
// empty string and a NULL are different inputs. The expected digests come
// from Hermes's own rule (computed independently below), not from the Go
// code under test, so collapsing the two states fails here.
func TestEmptyStringColumnsAreNotTreatedAsNull(t *testing.T) {
	// What Hermes hashes for this row: api_content and tool_call_id are
	// present-but-empty, so they appear in the payload.
	payload := map[string]any{"role": "assistant", "content": "done", "api_content": "", "tool_call_id": ""}
	lastFP := sha256Hex(canonicalJSON(payload))
	prefixFP := sha256Hex(canonicalJSON([]any{[]any{"assistant", lastFP}}))
	cfg, err := json.Marshal(map[string]any{"_usage_anchor": map[string]any{
		"prompt_tokens": 100, "completion_tokens": 5, "base_count": 1,
		"base_last_role": "assistant", "base_last_fp": lastFP, "base_prefix_fp": prefixFP,
	}})
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	db := openFixture(t, home)
	if _, err := db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens,output_tokens) VALUES ('s','m',?,900,100)`, string(cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages (session_id,role,content,api_content,tool_call_id,active) VALUES ('s','assistant','done','','',1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	got := ResolveUsageIn(home, "s")
	if got == nil || got.ContextTokens != 105 {
		t.Fatalf("empty-string columns rejected a valid anchor: %#v", got)
	}
}

// These fingerprints were produced by upstream SessionDB._decode_content,
// SessionDB._loaded_view_content, and agent.usage_anchor on persisted rows.
// They deliberately do not call this package's projection helpers.
func TestPersistedContentMatchesUpstreamLoadedProjection(t *testing.T) {
	cases := []struct {
		name, role, stored, fingerprint string
	}{
		{"trim_user_scalar", "user", "  42  ", "2759c380a85473f64dab55ebc03e43df9c8d996114a9c3d1d6c4daf3cc4e9365"},
		{"trim_assistant_json_text", "assistant", `  {"ok":true}  `, "91a6121f5607fae218e6cc23de7a1cbb7d3dc9931af6fd6f57a45fe226ed569e"},
		{"plain_tool_json_stays_text", "tool", `{"result":42}`, "e8059a8f3f6f250c991899c70118e4290eddc312a5302849c629ba5b0d630f38"},
		{"sentinel_multimodal_decodes", "user", "\x00json:[{\"type\":\"text\",\"text\":\"hello\"}]", "605b2cd3bceaf2d08e7ba22785ed603b5cce260a16f3215a2b9751da809810e2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			db := openFixture(t, home)
			cfg, err := json.Marshal(map[string]any{"_usage_anchor": map[string]any{
				"prompt_tokens": 1000, "completion_tokens": 10, "base_count": 1,
				"base_last_role": tc.role, "base_last_fp": tc.fingerprint,
				"base_prefix_fp": sha256Hex(canonicalJSON([]any{[]any{tc.role, tc.fingerprint}})),
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO sessions (id,model,model_config) VALUES ('s','m',?)`, string(cfg)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO messages (session_id,role,content,active) VALUES ('s',?,?,1)`, tc.role, tc.stored); err != nil {
				t.Fatal(err)
			}
			db.Close()
			got := ResolveUsageIn(home, "s")
			if got == nil || got.ContextUnavailable || got.ContextTokens != 1010 {
				t.Fatalf("upstream anchor rejected: %#v", got)
			}
		})
	}
}

func TestUsageFieldsAndWindowResolution(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	_, err := db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,billing_provider,billing_base_url,billing_mode,cost_status,estimated_cost_usd,actual_cost_usd)
VALUES ('s','openai/model-x','{"base_url":"https://gateway.example/v1"}',100,40,300,100,25,'llm-rosetta','https://ignored.example/v1','chat_completions','actual',1.25,2.5)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.WriteFile(filepath.Join(home, "context_length_cache.yaml"), []byte("context_lengths:\n  openai/model-x@https://gateway.example/v1: 200000\n  model-x@https://gateway.example/v1: 180000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := ResolveUsageIn(home, "s")
	if got == nil || got.Billing == nil {
		t.Fatal("nil usage")
	}
	if got.Billing.Tokens != 540 {
		t.Fatalf("tokens=%d; reasoning was double-counted", got.Billing.Tokens)
	}
	if got.Billing.CostUSD != 2.5 || got.Billing.Backend != "llm-rosetta" {
		t.Fatalf("billing=%#v", got.Billing)
	}
	if got.Billing.Class != core.BillingClassPayAsYouGo {
		t.Fatalf("actual cost status must classify pay-as-you-go, got %v", got.Billing.Class)
	}
	if got.WindowTokens == nil || *got.WindowTokens != 180000 {
		t.Fatalf("conservative alias window=%v", got.WindowTokens)
	}
	if got.SessionCache == nil || got.SessionCache.HitPercent != 60 {
		t.Fatalf("cache=%#v", got.SessionCache)
	}
}

// Hermes billing classification follows persisted cost evidence. billing_mode
// is consulted only for the explicit subscription marker; other values are
// API transports, not billing classes.
func TestBillingClassificationUsesPersistedCostEvidence(t *testing.T) {
	for _, tc := range []struct {
		status, mode string
		want         core.BillingClass
	}{
		{"actual", "", core.BillingClassPayAsYouGo},
		{"estimated", "", core.BillingClassPayAsYouGo},
		{"actual", "subscription_included", core.BillingClassSubscription},
		{"unknown", "subscription_included", core.BillingClassSubscription},
		{"unknown", "codex_responses", core.BillingClassUnknown},
		{"", "chat_completions", core.BillingClassUnknown},
	} {
		if got := classifyBilling(tc.status, tc.mode); got != tc.want {
			t.Errorf("status=%q mode=%q: got %v, want %v", tc.status, tc.mode, got, tc.want)
		}
	}
}

func TestResolveSessionBillingNeedsOnlySessionsTable(t *testing.T) {
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (
 id TEXT PRIMARY KEY, input_tokens INTEGER, output_tokens INTEGER,
 billing_provider TEXT, billing_base_url TEXT, billing_mode TEXT,
 cost_status TEXT, estimated_cost_usd REAL, actual_cost_usd REAL);
INSERT INTO sessions VALUES ('s',12,3,'openrouter','https://openrouter.ai/api/v1',NULL,'estimated',0.25,0)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("HERMES_HOME", home)
	got, ok := Provider.ResolveSessionBilling(provider.UsageResolveInput{Session: &provider.AgentSession{Kind: "id", Value: "s"}})
	if !ok || got.Class != core.BillingClassPayAsYouGo || got.Tokens != 15 || got.CostUSD != 0.25 || got.Backend != "openrouter" {
		t.Fatalf("billing=%#v ok=%v", got, ok)
	}
}

func TestUnknownBillingClassPreservesSessionFacts(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	_, err := db.Exec(`INSERT INTO sessions
 (id,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,billing_provider,billing_mode,cost_status,estimated_cost_usd,actual_cost_usd)
 VALUES ('unknown-billing',100000,25000,300000,50000,'llm-rosetta','chat_completions','unknown',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("HERMES_HOME", home)

	got, ok := Provider.ResolveSessionBilling(provider.UsageResolveInput{Session: &provider.AgentSession{Kind: "id", Value: "unknown-billing"}})
	if !ok || got.Class != core.BillingClassUnknown || got.Backend != "llm-rosetta" || got.Tokens != 475000 || got.CostUSD != 0 {
		t.Fatalf("billing=%#v ok=%v", got, ok)
	}
}

func TestExplicitAndUnknownContextWindows(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	_, _ = db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens) VALUES ('explicit','x','{"context_length":12345}',1),('unknown','y','{}',2)`)
	db.Close()
	if got := ResolveUsageIn(home, "explicit"); got.WindowTokens == nil || *got.WindowTokens != 12345 {
		t.Fatalf("explicit=%#v", got)
	}
	if got := ResolveUsageIn(home, "unknown"); got == nil || got.WindowTokens != nil {
		t.Fatalf("unknown=%#v", got)
	}
}

func TestProfileIsolationAndProviderSessionKind(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	for _, tc := range []struct {
		home string
		n    int
	}{{homeA, 3}, {homeB, 9}} {
		db := openFixture(t, tc.home)
		_, _ = db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens) VALUES ('same','m','{}',?)`, tc.n)
		db.Close()
	}
	t.Setenv("HERMES_HOME", homeB)
	got := Provider.ResolveUsage(provider.UsageResolveInput{Session: &provider.AgentSession{Kind: "id", Value: "same"}})
	if sessionTokens(t, got) != 9 {
		t.Fatalf("profile result=%#v", got)
	}
	if got := Provider.ResolveUsage(provider.UsageResolveInput{Session: &provider.AgentSession{Kind: "path", Value: "same"}}); got != nil {
		t.Fatalf("non-id=%#v", got)
	}
	billing, ok := Provider.ResolveSessionBilling(provider.UsageResolveInput{Session: &provider.AgentSession{Kind: "id", Value: "same"}})
	if !ok || billing.Tokens != 9 {
		t.Fatalf("session billing=%#v ok=%v", billing, ok)
	}
	if _, ok := Provider.ResolveSessionBilling(provider.UsageResolveInput{Session: &provider.AgentSession{Kind: "id", Value: "absent"}}); ok {
		t.Fatal("absent session must report no billing evidence")
	}
}

func TestCostAndBaseURLFallback(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	_, _ = db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens,billing_base_url,billing_mode,estimated_cost_usd) VALUES ('s','m','{}',1,'https://api.deepseek.com/v1','chat_completions',0.75)`)
	db.Close()
	got := ResolveUsageIn(home, "s")
	if got == nil || got.Billing == nil || got.Billing.CostUSD != .75 || got.Billing.Backend != "deepseek" {
		t.Fatalf("fallback=%#v", got)
	}
}

// A local OpenAI-compatible server has no registrable domain; its literal
// address must stay intact rather than collapsing to a dotted fragment.
func TestBackendIdentityKeepsLiteralAddresses(t *testing.T) {
	for _, tc := range []struct{ baseURL, want string }{
		{"http://127.0.0.1:8000/v1", "127.0.0.1"},
		{"http://[::1]:8000/v1", "::1"},
		{"https://api.deepseek.com/v1", "deepseek"},
		{"http://localhost:1234/v1", "localhost"},
	} {
		if got := backendIdentity("", tc.baseURL); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.baseURL, got, tc.want)
		}
	}
	if got := backendIdentity("llm-rosetta", "http://127.0.0.1:8000/v1"); got != "llm-rosetta" {
		t.Errorf("recorded provider must win: %q", got)
	}
}

// Hermes hashes the Python object, where a tool-call float 5.0 serializes
// as 5.0; re-encoding it as 5 would reject an otherwise valid anchor.
func TestToolCallFloatsPreserveAnchorValidity(t *testing.T) {
	home := t.TempDir()
	db := openFixture(t, home)
	toolCalls := `[{"id":"c1","function":{"name":"f","arguments":"{}"},"index":5.0}]`
	base := []message{{Role: "assistant", Content: text("calling"), ToolCalls: decodeJSON(toolCalls)}}
	cfg := anchorConfig(t, base, 100, 5)
	if _, err := db.Exec(`INSERT INTO sessions (id,model,model_config,input_tokens,output_tokens) VALUES ('s','m',?,900,100)`, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO messages (session_id,role,content,tool_calls,active) VALUES ('s','assistant','calling',?,1)`, toolCalls); err != nil {
		t.Fatal(err)
	}
	db.Close()
	got := ResolveUsageIn(home, "s")
	if got == nil || got.ContextTokens != 105 {
		t.Fatalf("float tool-call anchor rejected: %#v", got)
	}
}
