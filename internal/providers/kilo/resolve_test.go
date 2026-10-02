/**
 * Tests for Kilo context resolution against a real-shaped session store.
 *
 * The fixtures mirror the rows Kilo actually writes: step-finish parts carrying
 * per-step context, assistant messages naming the backend, and the denormalised
 * session totals Kilo backfills from its messages.
 */
package kilo

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// A step-finish row as Kilo writes it: prompt-cache occupancy for that step,
// with the model block only present on newer rows.
const stepWithModel = `{"reason":"tool-calls","type":"step-finish","time":{"start":1,"end":2,"elapsed":5592},
 "model":{"providerID":"kilo","modelID":"~openai/gpt-mini-latest"},
 "metrics":{"generation":79.04,"source":"computed"},
 "tokens":{"total":30494,"input":1219,"output":442,"reasoning":0,"cache":{"write":0,"read":28833}},
 "cost":0}`

// The same step on an older row, where the model is only on the message.
const stepWithoutModel = `{"reason":"tool-calls","type":"step-finish","time":{"start":1,"end":2,"elapsed":1},
 "tokens":{"total":74588,"input":74566,"output":4,"reasoning":18,"cache":{"write":0,"read":0}},
 "cost":0}`

const assistantMessage = `{"role":"assistant","mode":"general","cost":0,
 "tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},
 "modelID":"~openai/gpt-mini-latest","providerID":"kilo"}`

// A free-model step: every counter is zero. Reporting a 0-token context would
// present as an untouched window, so this must yield no usage at all.
const costOnlyStep = `{"type":"step-finish","tokens":{"total":0,"input":0,"output":0,"reasoning":0,
 "cache":{"read":0,"write":0}},"cost":0}`

// writeStore builds a Kilo session store with the given session rows.
func writeStore(t *testing.T, steps []string, messages []string, sessionCols [6]int, modelJSON any) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kilo.db")
	db := openStoreDB(t, dbPath)
	insertSession(t, db, "ses_test", "/repo", 1, modelJSON)
	insertSessionTotals(t, db, "ses_test", sessionCols)
	for i, data := range messages {
		insertMessage(t, db, "msg_"+string(rune('a'+i)), "ses_test", int64(100-i), data)
	}
	for i, data := range steps {
		insertPart(t, db, "prt_"+string(rune('a'+i)), "msg_a", "ses_test", int64(100-i), data)
	}
	closeStoreDB(t, db)
	return dbPath
}

// writeSessions builds a store holding one live session per directory, each with
// a step-finish part of its own. It is the fixture for everything that turns on
// *which* session a pane may claim, where the sessions have to be told apart.
func writeSessions(t *testing.T, directories ...string) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kilo.db")
	db := openStoreDB(t, dbPath)
	for i, directory := range directories {
		id := "ses_" + string(rune('a'+i))
		insertSession(t, db, id, directory, int64(i+1), "")
		insertMessage(t, db, "msg_"+id, id, 1, assistantMessage)
		insertPart(t, db, "prt_"+id, "msg_"+id, id, 1, stepWithModel)
	}
	closeStoreDB(t, db)
	return dbPath
}

func openStoreDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	createStoreSchema(t, db)
	return db
}

func closeStoreDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func createStoreSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `CREATE TABLE session (
		id TEXT PRIMARY KEY, directory TEXT, time_updated INTEGER DEFAULT 0,
		time_archived INTEGER, cost REAL DEFAULT 0 NOT NULL,
		tokens_input INTEGER DEFAULT 0 NOT NULL, tokens_output INTEGER DEFAULT 0 NOT NULL,
		tokens_reasoning INTEGER DEFAULT 0 NOT NULL, tokens_cache_read INTEGER DEFAULT 0 NOT NULL,
		tokens_cache_write INTEGER DEFAULT 0 NOT NULL, model TEXT)`)
	mustExec(t, db, `CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT,
		time_created INTEGER, data TEXT)`)
	mustExec(t, db, `CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT,
		time_created INTEGER, data TEXT)`)
	mustExec(t, db, `CREATE INDEX part_session_step_finish_idx ON part (session_id)
		WHERE json_valid(part.data) AND json_extract(part.data,'$.type') = 'step-finish'`)
}

// insertSession writes one session row. A nil model writes SQL NULL, which is
// what Kilo writes for a session whose model it never recorded.
func insertSession(t *testing.T, db *sql.DB, id, directory string, timeUpdated int64, model any) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO session (id, directory, time_updated, model) VALUES (?, ?, ?, ?)`,
		id, directory, timeUpdated, model)
	if err != nil {
		t.Fatal(err)
	}
}

func insertSessionTotals(t *testing.T, db *sql.DB, id string, cols [6]int) {
	t.Helper()
	// cost, then the five token counters Kilo backfills from the messages.
	_, err := db.Exec(
		`UPDATE session SET cost = 0.5, tokens_input = ?, tokens_output = ?, tokens_reasoning = ?,
		 tokens_cache_read = ?, tokens_cache_write = ? WHERE id = ?`,
		cols[0], cols[1], cols[2], cols[3], cols[4], id)
	if err != nil {
		t.Fatal(err)
	}
}

func insertMessage(t *testing.T, db *sql.DB, id, sessionID string, created int64, data string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO message (id, session_id, time_created, data) VALUES (?,?,?,?)`,
		id, sessionID, created, data); err != nil {
		t.Fatal(err)
	}
}

func insertPart(t *testing.T, db *sql.DB, id, messageID, sessionID string, created int64, data string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO part (id, message_id, session_id, time_created, data) VALUES (?,?,?,?,?)`,
		id, messageID, sessionID, created, data); err != nil {
		t.Fatal(err)
	}
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func useStore(t *testing.T, dbPath string) {
	t.Helper()
	t.Setenv("KILO_DB", dbPath)
	t.Setenv("KILO_DATA_DIR", "")
}

func strPtr(s string) *string { return &s }

func TestResolveUsage_NewestStepCarriesContextAndWindow(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models.json")
	if err := os.WriteFile(models, []byte(
		`{"kilo":{"models":{"~openai/gpt-mini-latest":{"limit":{"context":1000000}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILO_MODELS_PATH", models)
	ClearModelsCatalogCache()

	dbPath := writeStore(t, []string{stepWithModel}, []string{assistantMessage}, [6]int{}, "")
	useStore(t, dbPath)

	usage := ResolveUsageForKilo(strPtr("ses_test"), strPtr("/repo"))
	if usage == nil {
		t.Fatal("no usage")
	}
	// Context is prompt-cache occupancy: input + cache read + cache write. The
	// 442 output tokens are already inside the next step's input, so counting
	// them here would double-count the window.
	if usage.ContextTokens != 1219+28833 {
		t.Fatalf("context tokens = %d", usage.ContextTokens)
	}
	if usage.WindowTokens == nil || *usage.WindowTokens != 1000000 {
		t.Fatalf("window = %v", usage.WindowTokens)
	}
	if usage.Cache == nil || usage.Cache.ReadTokens != 28833 || usage.Cache.FreshInputTokens != 1219 {
		t.Fatalf("cache = %+v", usage.Cache)
	}
}

func TestResolveUsage_ModelComesFromTheMessageWhenTheStepHasNone(t *testing.T) {
	// Only 88 of Kilo's 9140 step rows carry their own model block, so the
	// message is the normal source of the backend name.
	t.Setenv("KILO_MODELS_PATH", filepath.Join(t.TempDir(), "absent.json"))
	ClearModelsCatalogCache()

	dbPath := writeStore(t, []string{stepWithoutModel}, []string{assistantMessage}, [6]int{}, "")
	useStore(t, dbPath)

	usage := ResolveUsageForKilo(strPtr("ses_test"), strPtr("/repo"))
	if usage == nil {
		t.Fatal("no usage")
	}
	if usage.WindowTokens != nil {
		t.Fatalf("unknown model must yield no window, got %v", *usage.WindowTokens)
	}
	if backend := BackendForKilo(strPtr("ses_test"), nil); backend != "kilo" {
		t.Fatalf("backend = %q", backend)
	}
}

func TestResolveUsage_CostOnlyStepYieldsNoUsage(t *testing.T) {
	// Kilo records free-model steps with every counter at zero. Reading that as
	// a 0-token context would present as an untouched window.
	dbPath := writeStore(t, []string{costOnlyStep}, []string{assistantMessage}, [6]int{}, "")
	useStore(t, dbPath)

	if usage := ResolveUsageForKilo(strPtr("ses_test"), strPtr("/repo")); usage != nil {
		t.Fatalf("cost-only step produced usage: %+v", usage)
	}
}

func TestResolveUsage_UnknownSessionFallsBackToCwd(t *testing.T) {
	// Herdr captures the session id at launch and never refreshes it, so a
	// cleared or resumed session reports an id that no longer exists.
	dbPath := writeStore(t, []string{stepWithModel}, []string{assistantMessage}, [6]int{}, "")
	useStore(t, dbPath)

	if usage := ResolveUsageForKilo(strPtr("ses_stale"), strPtr("/repo")); usage == nil {
		t.Fatal("cwd fallback did not recover the session")
	}
	if usage := ResolveUsageForKilo(strPtr("ses_stale"), strPtr("/elsewhere")); usage != nil {
		t.Fatalf("wrong cwd resolved usage: %+v", usage)
	}
}

func TestResolveUsage_NoIdentifiersYieldsNothing(t *testing.T) {
	dbPath := writeStore(t, []string{stepWithModel}, []string{assistantMessage}, [6]int{}, "")
	useStore(t, dbPath)

	if usage := ResolveUsageForKilo(nil, nil); usage != nil {
		t.Fatalf("no identifiers produced usage: %+v", usage)
	}
	if usage := ResolveUsageForKilo(strPtr(""), strPtr("")); usage != nil {
		t.Fatalf("blank identifiers produced usage: %+v", usage)
	}
}

func TestResolveUsage_TwoPanesInOneRepoDoNotCrossAttribute(t *testing.T) {
	// Two live Kilo panes in one repository share a cwd, so a pane whose
	// reported id is gone must not borrow the other pane's session by directory.
	// Once the id is stale the newest row in that directory is the other pane's,
	// so recovering from it would report a confident, wrong reading — for
	// context and for billing alike.
	dbPath := writeSessions(t, "/repo", "/repo")
	useStore(t, dbPath)

	// A pinned id is still its own session.
	if usage := ResolveUsageForKilo(strPtr("ses_a"), nil); usage == nil {
		t.Fatal("id-only resolution failed")
	}
	// A directory holding two live sessions attributes nothing.
	if usage := ResolveUsageForKilo(strPtr("ses_gone"), strPtr("/repo")); usage != nil {
		t.Fatalf("an ambiguous directory attributed another pane's session: %+v", usage)
	}
	if usage := ResolveUsageForKilo(nil, strPtr("/repo")); usage != nil {
		t.Fatalf("an ambiguous directory attributed a session: %+v", usage)
	}
	if backend := BackendForKilo(strPtr("ses_gone"), strPtr("/repo")); backend != "" {
		t.Fatalf("billing mode named a backend for an unattributable pane: %q", backend)
	}
}

func TestResolveUsage_CwdFallbackStaysInsideTheDirectoryTree(t *testing.T) {
	// The fallback reaches the directory and its descendants, which is how a
	// worktree checked out under the repo is covered. A bare prefix would also
	// reach siblings, so /repo must never answer for /repo-other; and without an
	// ESCAPE clause SQLite reads "_" and "%" in a path as wildcards.
	t.Setenv("KILO_MODELS_PATH", filepath.Join(t.TempDir(), "absent.json"))
	ClearModelsCatalogCache()

	t.Run("a descendant resolves", func(t *testing.T) {
		useStore(t, writeSessions(t, "/repo/worktrees/wt"))
		if usage := ResolveUsageForKilo(strPtr("ses_gone"), strPtr("/repo")); usage == nil {
			t.Fatal("a worktree under the directory did not resolve")
		}
	})
	t.Run("a sibling does not", func(t *testing.T) {
		// Two stores, because the point is that /repo-other is out of scope for
		// a pane in /repo: an in-scope /repo session must win, and with none,
		// nothing may be invented from the sibling.
		useStore(t, writeSessions(t, "/repo", "/repo-other"))
		if usage := ResolveUsageForKilo(strPtr("ses_gone"), strPtr("/repo")); usage == nil {
			t.Fatal("the session in the pane's own directory did not resolve")
		}
		useStore(t, writeSessions(t, "/repo-other"))
		if usage := ResolveUsageForKilo(strPtr("ses_gone"), strPtr("/repo")); usage != nil {
			t.Fatalf("a sibling directory resolved: %+v", usage)
		}
	})
	t.Run("a wildcard in the path is a literal", func(t *testing.T) {
		for _, directory := range []string{"/my_repo", "/100%done"} {
			useStore(t, writeSessions(t, filepath.Join(directory, "nested")))
			if usage := ResolveUsageForKilo(strPtr("ses_gone"), strPtr(directory)); usage == nil {
				t.Fatalf("%s: a descendant of a wildcarded path did not resolve", directory)
			}
		}
	})
	t.Run("a wildcard in the path cannot invent a match", func(t *testing.T) {
		// "/my_repo" must not stand in for "/myXrepo": the underscore is a path
		// character here, not a single-character wildcard.
		useStore(t, writeSessions(t, "/myXrepo"))
		if usage := ResolveUsageForKilo(strPtr("ses_gone"), strPtr("/my_repo")); usage != nil {
			t.Fatalf("an underscore was read as a wildcard: %+v", usage)
		}
	})
}

func TestBackendForKilo_RecoversTheSessionByDirectory(t *testing.T) {
	// Billing mode and context must name the same session. When herdr's id has
	// gone stale, a pane that shows a Kilo context but an empty backend would be
	// classified as Unknown and could appear next to the Kilo allowance instead of
	// as the pay-as-you-go session it is.
	dbPath := writeSessions(t, "/repo")
	useStore(t, dbPath)

	if backend := BackendForKilo(strPtr("ses_gone"), strPtr("/repo")); backend != "kilo" {
		t.Fatalf("backend = %q, want the recovered session's", backend)
	}
	if backend := BackendForKilo(strPtr("ses_gone"), nil); backend != "" {
		t.Fatalf("a session that cannot be resolved produced a backend: %q", backend)
	}
}

func TestResolveUsage_AbsentStoreYieldsNothing(t *testing.T) {
	t.Setenv("KILO_DB", filepath.Join(t.TempDir(), "absent.db"))
	if usage := ResolveUsageForKilo(strPtr("ses_test"), strPtr("/repo")); usage != nil {
		t.Fatalf("absent store produced usage: %+v", usage)
	}
	t.Setenv("KILO_DB", "")
	t.Setenv("KILO_DATA_DIR", t.TempDir())
	if usage := ResolveUsageForKilo(strPtr("ses_test"), strPtr("/repo")); usage != nil {
		t.Fatalf("empty data dir produced usage: %+v", usage)
	}
}

func TestSessionSummary_ReadsDenormalisedTotals(t *testing.T) {
	dbPath := writeStore(t, []string{stepWithModel}, []string{assistantMessage},
		[6]int{10, 20, 30, 40, 50},
		`{"id":"mimo-v2.6-pro","providerID":"opencode-go","variant":"default"}`)
	useStore(t, dbPath)

	summary, ok := SessionSummaryForKilo(strPtr("ses_test"))
	if !ok {
		t.Fatal("summary not read")
	}
	if summary.Cost != 0.5 {
		t.Fatalf("cost = %v", summary.Cost)
	}
	if summary.TotalTokens() != 150 {
		t.Fatalf("total tokens = %d", summary.TotalTokens())
	}
	if summary.ProviderID != "opencode-go" || summary.ModelID != "mimo-v2.6-pro" {
		t.Fatalf("identity = %+v", summary)
	}
	if _, ok := SessionSummaryForKilo(strPtr("ses_absent")); ok {
		t.Fatal("absent session produced a summary")
	}
	if _, ok := SessionSummaryForKilo(nil); ok {
		t.Fatal("nil session id produced a summary")
	}
}

func TestSessionSummary_EmptyModelColumnIsNormal(t *testing.T) {
	// session.model is populated on only some sessions; an empty value must
	// leave the identity blank rather than fail the read.
	dbPath := writeStore(t, []string{stepWithModel}, []string{assistantMessage}, [6]int{}, "")
	useStore(t, dbPath)

	summary, ok := SessionSummaryForKilo(strPtr("ses_test"))
	if !ok {
		t.Fatal("summary not read")
	}
	if summary.ProviderID != "" || summary.ModelID != "" {
		t.Fatalf("identity invented: %+v", summary)
	}
}

func TestResolveUsage_ModelComesFromTheMessageThatOwnsTheStep(t *testing.T) {
	// After a model switch the newest assistant message can belong to a call
	// that has not completed a step yet. Reading the identity from the session's
	// newest message would pair the previous model's tokens with the new model's
	// window — a false low percentage, or an overflow.
	models := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(models, []byte(
		`{"kilo":{"models":{"~openai/gpt-mini-latest":{"limit":{"context":1000000}}}},
		   "anthropic":{"models":{"claude-sonnet-5":{"limit":{"context":200000}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILO_MODELS_PATH", models)
	ClearModelsCatalogCache()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "kilo.db")
	db := openStoreDB(t, dbPath)
	insertSession(t, db, "ses_test", "/repo", 1, "")
	// The step belongs to the earlier message and was served by the earlier model.
	insertMessage(t, db, "msg_a", "ses_test", 1, assistantMessage)
	insertPart(t, db, "prt_a", "msg_a", "ses_test", 1, stepWithoutModel)
	// A newer turn switched model and has not completed a step.
	insertMessage(t, db, "msg_b", "ses_test", 2,
		`{"role":"assistant","providerID":"anthropic","modelID":"claude-sonnet-5",
		  "tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}`)
	closeStoreDB(t, db)
	useStore(t, dbPath)

	usage := ResolveUsageForKilo(strPtr("ses_test"), strPtr("/repo"))
	if usage == nil {
		t.Fatal("no usage")
	}
	// The step's own model, not the pane's newest one.
	if usage.WindowTokens == nil || *usage.WindowTokens != 1000000 {
		t.Fatalf("window = %v, want the step's own model", usage.WindowTokens)
	}
	// Billing mode still follows the live session, which is the newer message.
	if backend := BackendForKilo(strPtr("ses_test"), strPtr("/repo")); backend != "anthropic" {
		t.Fatalf("live backend = %q, want the newest assistant message's", backend)
	}
}

func TestSessionSummary_ToleratesANullModelColumn(t *testing.T) {
	// session.model is populated on only some sessions, and an unwritten one is
	// SQL NULL rather than an empty string. The read has to survive it: the
	// totals beside it are the pane's spend.
	dbPath := writeStore(t, []string{stepWithModel}, []string{assistantMessage}, [6]int{7, 0, 0, 20, 0}, nil)
	useStore(t, dbPath)

	summary, ok := SessionSummaryForKilo(strPtr("ses_test"))
	if !ok {
		t.Fatal("a NULL model column dropped the session's totals")
	}
	if summary.TotalTokens() != 27 {
		t.Fatalf("total tokens = %d", summary.TotalTokens())
	}
	if summary.ProviderID != "" || summary.ModelID != "" {
		t.Fatalf("identity invented: %+v", summary)
	}
}

func TestSessionActivity_NamesThePanesOwnSession(t *testing.T) {
	// Pane activity has to come from the same session the pane's context and
	// billing mode do, or the sidebar sums one session while showing another's.
	dbPath := writeSessions(t, "/repo")
	useStore(t, dbPath)

	summary, ok := SessionActivityForKilo(strPtr("ses_gone"), strPtr("/repo"))
	if !ok {
		t.Fatal("the recovered session reported no totals")
	}
	if summary.Cost != 0 {
		t.Fatalf("cost = %v, want the fixture's zero", summary.Cost)
	}
	if _, ok := SessionActivityForKilo(strPtr("ses_gone"), nil); ok {
		t.Fatal("an unresolvable pane reported session totals")
	}
}

func TestQueriesAreBoundedAndReadOnly(t *testing.T) {
	// A whole-table scan would be a bug: the store is a live WAL database with
	// hundreds of thousands of rows.
	for _, query := range []string{stepQuery, identityQuery, summaryQuery} {
		if !contains(query, "session_id = ?") && !contains(query, "WHERE id = ?") {
			t.Fatalf("query is not scoped to one session: %s", query)
		}
	}
	if !contains(stepQuery, "LIMIT ?") || !contains(identityQuery, "LIMIT 1") {
		t.Fatalf("queries are unbounded: %s / %s", stepQuery, identityQuery)
	}
	if contains(stepQuery, "SUM(") {
		t.Fatalf("step query aggregates instead of reading the newest row")
	}
	// The step's identity is read from one named message, so it is keyed on the
	// primary key rather than on a session-wide "newest" scan.
	if !contains(partIdentityQuery, "WHERE m.id = ?") || !contains(partIdentityQuery, "LIMIT 1") {
		t.Fatalf("step identity is not keyed on one message: %s", partIdentityQuery)
	}
	// The directory fallback must stay bounded and must keep its two-row read:
	// deciding whether a directory is shared needs to see that it is.
	if !contains(directoryScopeQuery, "LIMIT 2") {
		t.Fatalf("directory scope cannot detect ambiguity: %s", directoryScopeQuery)
	}
	if !contains(directoryScopeQuery, "ESCAPE") {
		t.Fatalf("directory scope reads wildcards as path characters: %s", directoryScopeQuery)
	}
	// The scope is the directory plus a separator-bounded prefix, never a bare
	// prefix, which would also match sibling directories.
	if !contains(directoryScopeQuery, "directory = ?") || !contains(directoryScopeQuery, "directory LIKE ?") {
		t.Fatalf("directory scope lost the exact match: %s", directoryScopeQuery)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
