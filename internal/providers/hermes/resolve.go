/**
 * Reads one Hermes Agent session from a profile-local state database.
 *
 * Hermes stores every session in SQLite under its profile home, so the
 * adapter opens that database read-only and resolves exactly the session id
 * Herdr reported. Column presence is probed at runtime because the Hermes
 * schema gains columns across releases; a missing optional column degrades
 * one field rather than the whole read.
 */
package hermes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/senna-lang/herdr-agent-usage/internal/core"
	_ "modernc.org/sqlite"
)

type sessionRow struct {
	model, modelConfig, billingProvider, billingBaseURL, billingMode, costStatus string
	input, output, cacheRead, cacheWrite                                         int
	actualCost, estimatedCost                                                    float64
}

type message struct {
	Role string
	// Nullable columns keep their SQL null state: Hermes's fingerprint
	// includes any field that is not None, so an empty string and a NULL
	// produce different digests and must not be conflated.
	Content, APIContent, ToolCallID sql.NullString
	ToolCalls                       any
}

// ResolveHome returns the active Hermes profile home. HERMES_HOME is the
// authoritative profile boundary; the default is ~/.hermes.
func ResolveHome() string {
	if home := os.Getenv("HERMES_HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hermes")
}

// ResolveUsageIn reads one exact session from a Hermes profile home.
func ResolveUsageIn(home, sessionID string) *core.ContextUsage {
	if home == "" || sessionID == "" {
		return nil
	}
	db := openStateDB(home)
	if db == nil {
		return nil
	}
	defer db.Close()
	row, ok := readSessionRow(db, sessionID)
	if !ok {
		return nil
	}

	messages := activeMessages(db, sessionID)
	contextTokens := anchoredTokens(row.modelConfig, messages)
	usage := &core.ContextUsage{
		ContextUnavailable: contextTokens == nil,
		SessionCache:       core.CacheFromTokenCounts(nonNegative(row.input), nonNegative(row.cacheRead), nonNegative(row.cacheWrite)),
		Billing:            billingFromRow(row),
	}
	if contextTokens != nil {
		usage.ContextTokens = *contextTokens
	}
	usage.WindowTokens = contextWindow(home, row.model, row.modelConfig, row.billingBaseURL)
	if usage.ContextTokens == 0 && sessionTokenCount(row) == 0 && usage.SessionCache == nil &&
		usage.Billing.Backend == "" && usage.WindowTokens == nil {
		return nil
	}
	return usage
}

func openStateDB(home string) *sql.DB {
	dbPath := filepath.Join(home, "state.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil
	}
	return db
}

func readSessionRow(db *sql.DB, sessionID string) (sessionRow, bool) {
	cols := tableColumns(db, "sessions")
	if !cols["id"] {
		return sessionRow{}, false
	}
	expr := func(name, fallback string) string {
		if cols[name] {
			return "COALESCE(" + name + ", " + fallback + ")"
		}
		return fallback
	}
	query := `SELECT ` + strings.Join([]string{
		expr("model", "''"), expr("model_config", "''"), expr("billing_provider", "''"),
		expr("billing_base_url", "''"), expr("billing_mode", "''"), expr("cost_status", "''"), expr("input_tokens", "0"),
		expr("output_tokens", "0"), expr("cache_read_tokens", "0"), expr("cache_write_tokens", "0"),
		expr("actual_cost_usd", "0"), expr("estimated_cost_usd", "0"),
	}, ", ") + ` FROM sessions WHERE id = ? LIMIT 1`
	var row sessionRow
	if err := db.QueryRow(query, sessionID).Scan(&row.model, &row.modelConfig, &row.billingProvider,
		&row.billingBaseURL, &row.billingMode, &row.costStatus, &row.input, &row.output, &row.cacheRead,
		&row.cacheWrite, &row.actualCost, &row.estimatedCost); err != nil {
		return sessionRow{}, false
	}
	return row, true
}

func sessionTokenCount(row sessionRow) int {
	return nonNegative(row.input) + nonNegative(row.cacheRead) + nonNegative(row.cacheWrite) + nonNegative(row.output)
}

func billingFromRow(row sessionRow) *core.SessionBilling {
	return &core.SessionBilling{
		Class:   classifyBilling(row.costStatus, row.billingMode),
		Backend: backendIdentity(row.billingProvider, row.billingBaseURL),
		Tokens:  sessionTokenCount(row),
		CostUSD: preferredCost(row.actualCost, row.estimatedCost),
	}
}

func tableColumns(db *sql.DB, table string) map[string]bool {
	out := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var def any
		if rows.Scan(&cid, &name, &typ, &notnull, &def, &pk) == nil {
			out[name] = true
		}
	}
	return out
}

func activeMessages(db *sql.DB, sessionID string) []message {
	cols := tableColumns(db, "messages")
	if !cols["session_id"] || !cols["role"] || !cols["content"] {
		return nil
	}
	// A column absent from this schema version scans as NULL, which is what
	// Hermes recorded when the field did not exist.
	expr := func(name string) string {
		if cols[name] {
			return name
		}
		return "NULL"
	}
	where := "session_id = ?"
	if cols["active"] {
		where += " AND COALESCE(active, 1) = 1"
	}
	order := "rowid"
	if cols["id"] {
		order = "id"
	}
	rows, err := db.Query(`SELECT role, content, `+expr("api_content")+`, `+expr("tool_call_id")+`, `+expr("tool_calls")+` FROM messages WHERE `+where+` ORDER BY `+order, sessionID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []message
	for rows.Next() {
		var m message
		var toolCalls sql.NullString
		if rows.Scan(&m.Role, &m.Content, &m.APIContent, &m.ToolCallID, &toolCalls) != nil {
			continue
		}
		if toolCalls.Valid {
			// decodeJSON keeps numeric literals verbatim; a float such as 5.0
			// re-encoded as 5 would break fingerprint equality with Hermes.
			m.ToolCalls = decodeJSON(toolCalls.String)
		}
		out = append(out, m)
	}
	return out
}

func anchoredTokens(config string, messages []message) *int {
	var root map[string]any
	if json.Unmarshal([]byte(config), &root) != nil {
		return nil
	}
	a, ok := root["_usage_anchor"].(map[string]any)
	if !ok {
		return nil
	}
	pt, pok := positiveJSONInt(a["prompt_tokens"])
	bc, bok := positiveJSONInt(a["base_count"])
	ct, _ := jsonInt(a["completion_tokens"])
	lastFP, fok := a["base_last_fp"].(string)
	prefixFP, xok := a["base_prefix_fp"].(string)
	lastRole, _ := a["base_last_role"].(string)
	if !pok || !bok || !fok || !xok || bc > len(messages) || bc == 0 {
		return nil
	}
	if messages[bc-1].Role != lastRole || messageFingerprint(messages[bc-1]) != lastFP || prefixFingerprint(messages[:bc]) != prefixFP {
		return nil
	}
	total := pt + max(0, ct)
	delta := messages[bc:]
	if len(delta) > 0 && delta[0].Role == "assistant" {
		delta = delta[1:]
	}
	for _, m := range delta {
		total += estimateMessage(m)
	}
	return &total
}

// fingerprintPayload mirrors Hermes's _FINGERPRINT_KEYS projection: every
// provider-visible field that is not None, in Hermes's own value shapes.
func fingerprintPayload(m message) map[string]any {
	p := map[string]any{"role": m.Role}
	if m.Content.Valid {
		p["content"] = loadedContent(m.Role, m.Content.String)
	}
	if m.APIContent.Valid {
		p["api_content"] = m.APIContent.String
	}
	if m.ToolCallID.Valid {
		p["tool_call_id"] = m.ToolCallID.String
	}
	if m.ToolCalls != nil {
		p["tool_calls"] = m.ToolCalls
	}
	return p
}

func messageFingerprint(m message) string {
	sum := sha256.Sum256([]byte(canonicalJSON(fingerprintPayload(m))))
	return hex.EncodeToString(sum[:])
}

const contentJSONPrefix = "\x00json:"

// loadedContent mirrors SessionDB's durable-row projection before usage
// anchoring: only sentinel-prefixed structured content is decoded, while
// user and assistant strings are stripped on load.
func loadedContent(role, stored string) any {
	var content any = stored
	if strings.HasPrefix(stored, contentJSONPrefix) {
		if decoded := decodeJSON(strings.TrimPrefix(stored, contentJSONPrefix)); decoded != nil {
			content = decoded
		}
	}
	if text, ok := content.(string); ok && (role == "user" || role == "assistant") {
		return strings.TrimSpace(text)
	}
	return content
}

// decodeJSON parses a stored JSON payload preserving numeric literals, or
// returns nil when the value is not JSON. Literal preservation matters:
// Hermes hashes the Python object, where 5 and 5.0 serialize differently.
func decodeJSON(raw string) any {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	if dec.More() {
		return nil
	}
	return v
}

// canonicalJSON reproduces Hermes's fingerprint encoding: Python
// json.dumps(..., sort_keys=True, ensure_ascii=True, separators=(",", ":")).
// Go's encoding/json differs on both counts it matters for — it emits raw
// UTF-8 and HTML-escapes <, > and & — so a shared hash needs this encoder
// rather than json.Marshal.
func canonicalJSON(v any) string {
	var b strings.Builder
	writeCanonicalJSON(&b, v)
	return b.String()
}

func writeCanonicalJSON(b *strings.Builder, v any) {
	switch value := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(value))
	case string:
		writeCanonicalString(b, value)
	case json.Number:
		b.WriteString(value.String())
	case float64:
		b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	case []any:
		b.WriteByte('[')
		for i, item := range value {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonicalJSON(b, item)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonicalString(b, k)
			b.WriteByte(':')
			writeCanonicalJSON(b, value[k])
		}
		b.WriteByte('}')
	default:
		writeCanonicalString(b, fmt.Sprint(value))
	}
}

// writeCanonicalString mirrors Python's ensure_ascii escaping, including
// surrogate pairs for codepoints outside the basic plane.
func writeCanonicalString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(b, `\u%04x`, r)
			case r < 0x7f:
				b.WriteRune(r)
			case r <= 0xffff:
				fmt.Fprintf(b, `\u%04x`, r)
			default:
				r -= 0x10000
				fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			}
		}
	}
	b.WriteByte('"')
}

func prefixFingerprint(messages []message) string {
	pairs := make([]any, 0, len(messages))
	for _, m := range messages {
		pairs = append(pairs, []any{m.Role, messageFingerprint(m)})
	}
	sum := sha256.Sum256([]byte(canonicalJSON(pairs)))
	return hex.EncodeToString(sum[:])
}

func estimateMessage(m message) int {
	p := fingerprintPayload(m)
	// Mirrors Hermes's wire shadow: a non-empty api_content string displaces
	// content on user/assistant rows, because that is what ships.
	if (m.Role == "user" || m.Role == "assistant") && m.APIContent.String != "" {
		p["content"] = m.APIContent.String
		delete(p, "api_content")
	}
	return (len(canonicalJSON(p)) + 3) / 4
}

func contextWindow(home, model, config, baseURL string) *int {
	var root map[string]any
	if json.Unmarshal([]byte(config), &root) == nil {
		if n, ok := positiveJSONInt(root["context_length"]); ok {
			return &n
		}
		if runtime, ok := root["gateway_runtime"].(map[string]any); ok {
			if n, ok := positiveJSONInt(runtime["context_length"]); ok {
				return &n
			}
			if s, ok := runtime["base_url"].(string); ok && s != "" {
				baseURL = s
			}
		}
		if s, ok := root["base_url"].(string); ok && s != "" {
			baseURL = s
		}
	}
	raw, err := os.ReadFile(filepath.Join(home, "context_length_cache.yaml"))
	if err != nil || model == "" {
		return nil
	}
	entries := parseContextCache(string(raw))
	baseURL = strings.TrimRight(baseURL, "/")
	keys := []string{model + "@" + baseURL}
	if i := strings.Index(model, "/"); i > 0 {
		keys = append(keys, model[i+1:]+"@"+baseURL)
	}
	var best *int
	for _, key := range keys {
		if n, ok := entries[key]; ok && n > 0 && (best == nil || n < *best) {
			v := n
			best = &v
		}
	}
	return best
}

func parseContextCache(raw string) map[string]int {
	out := map[string]int{}
	in := false
	for _, line := range strings.Split(raw, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "context_lengths:" {
			in = true
			continue
		}
		if !in || trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if len(line) == len(strings.TrimLeft(line, " \t")) {
			break
		}
		i := strings.LastIndex(trim, ":")
		if i < 0 {
			continue
		}
		key := strings.Trim(strings.TrimSpace(trim[:i]), "'\"")
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(trim[i+1:]), "%d", &n); err == nil {
			out[key] = n
		}
	}
	return out
}

// backendIdentity names the billed backend. The recorded billing provider is
// authoritative (it preserves gateway identities such as llm-rosetta); the
// endpoint host is only a fallback label.
func backendIdentity(billingProvider, baseURL string) string {
	if billingProvider != "" {
		return billingProvider
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	// A literal address has no registrable domain to shorten; taking the
	// penultimate dotted component of 127.0.0.1 would label the backend "0".
	if net.ParseIP(host) != nil {
		return host
	}
	host = strings.TrimPrefix(host, "api.")
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return host
}

func preferredCost(actual, estimated float64) float64 {
	if actual > 0 {
		return actual
	}
	if estimated > 0 {
		return estimated
	}
	return 0
}
func nonNegative(n int) int {
	if n > 0 {
		return n
	}
	return 0
}
func jsonInt(v any) (int, bool)         { n, ok := v.(float64); return int(n), ok }
func positiveJSONInt(v any) (int, bool) { n, ok := jsonInt(v); return n, ok && n > 0 }
