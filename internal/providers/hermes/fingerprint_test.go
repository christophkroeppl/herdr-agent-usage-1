package hermes

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCanonicalJSONGoldenEncoding pins the exact encoding without needing a
// Python interpreter, so CI hosts without python3 still catch a regression
// to Go's default json.Marshal (raw UTF-8, HTML escaping).
func TestCanonicalJSONGoldenEncoding(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"html_escapes", map[string]any{"role": "user", "content": "a < b && c > d"},
			`{"content":"a < b && c > d","role":"user"}`},
		{"unicode", map[string]any{"content": "café"}, `{"content":"caf\u00e9"}`},
		{"astral", map[string]any{"content": "🚀"}, `{"content":"\ud83d\ude80"}`},
		{"control", map[string]any{"content": "a\nb\tc\x01"}, `{"content":"a\nb\tc\u0001"}`},
		{"key_order", map[string]any{"b": 1.0, "a": 2.0}, `{"a":2,"b":1}`},
	} {
		if got := canonicalJSON(tc.value); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestCanonicalJSONMatchesHermesPython pins the fingerprint encoding to the
// Python serialization Hermes actually hashes. Go's encoding/json emits raw
// UTF-8 and HTML-escapes <, > and &, so a divergence here silently rejects
// every anchor whose transcript contains such characters.
func TestCanonicalJSONMatchesHermesPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	cases := []struct {
		name  string
		value map[string]any
	}{
		{"ascii", map[string]any{"role": "user", "content": "plain text"}},
		{"html_escapes", map[string]any{"role": "user", "content": "a < b && c > d"}},
		{"unicode", map[string]any{"role": "assistant", "content": "日本語 · café · Привет"}},
		{"astral", map[string]any{"role": "user", "content": "emoji 🚀 tail"}},
		{"nested", map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "<b>&nbsp;</b>"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:ü"}},
		}}},
		{"control", map[string]any{"role": "tool", "content": "line\nnext\ttab\x01"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := canonicalJSON(tc.value)
			script := `import json,sys;print(json.dumps(json.loads(sys.stdin.read()),sort_keys=True,ensure_ascii=True,separators=(",",":")),end="")`
			cmd := exec.Command(python, "-c", script)
			cmd.Stdin = strings.NewReader(got)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("python re-encode failed: %v", err)
			}
			if string(out) != got {
				t.Fatalf("canonical JSON diverges from Hermes encoding:\n go     = %s\n python = %s", got, out)
			}
		})
	}
}

// TestFingerprintMatchesHermesDigest reproduces Hermes's own message
// fingerprint (agent/usage_anchor.message_fingerprint) for a Unicode message,
// proving anchor validation accepts anchors Hermes actually wrote.
func TestFingerprintMatchesHermesDigest(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	m := message{Role: "user", Content: text("статус <ok> & 🚀")}
	script := `
import hashlib, json
payload = {"role": "user", "content": "\u0441\u0442\u0430\u0442\u0443\u0441 <ok> & \U0001F680"}
raw = json.dumps(payload, sort_keys=True, default=str, ensure_ascii=True, separators=(",", ":"))
print(hashlib.sha256(raw.encode("utf-8", "replace")).hexdigest(), end="")
`
	out, err := exec.Command(python, "-c", script).Output()
	if err != nil {
		t.Fatalf("python digest failed: %v", err)
	}
	if got := messageFingerprint(m); got != string(out) {
		t.Fatalf("fingerprint mismatch:\n go     = %s\n hermes = %s", got, out)
	}
}
