package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/senna-lang/herdr-agent-usage/internal/herdrcli"
	"github.com/senna-lang/herdr-agent-usage/internal/providers/kilo"
	_ "modernc.org/sqlite"
)

func TestKiloCwdFallbackDoesNotBorrowPaneWithForegroundDirectory(t *testing.T) {
	dir := t.TempDir()
	// The pane-list fields retain both the launch cwd and the cwd of the
	// foreground process. The sidebar uses the latter to find Kilo's session.
	open := []herdrcli.RawPaneListEntry{{
		PaneID: "w1:b", Agent: "kilo", Cwd: "/repo", ForegroundCwd: "/repo",
		SessionKind: "id", SessionValue: "ses_b",
	}}
	original := kilo.ListOpenPanes
	kilo.ListOpenPanes = func() ([]kilo.OpenPaneClaim, bool) {
		return kiloOpenPaneClaims(herdrcli.BuildOpenAgentPanes(open, nil)), true
	}
	t.Cleanup(func() { kilo.ListOpenPanes = original })
	path := filepath.Join(dir, "kilo.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT, parent_id TEXT, time_updated INTEGER, time_archived INTEGER)`,
		`CREATE TABLE message (session_id TEXT, time_created INTEGER, data TEXT)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO session (id, directory, time_updated) VALUES ('ses_b', '/repo', ?)`, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message VALUES ('ses_b', 1, '{"role":"assistant","providerID":"deepseek"}')`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILO_DB", path)
	cwd := "/repo"
	// A pane-list snapshot can omit the requesting pane while it starts up.
	// A candidate whose id another listed pane already claims is never ours.
	if got := kilo.BackendForKilo(nil, &cwd); got != "" {
		t.Fatalf("pane A borrowed an explicitly claimed backend %q", got)
	}
	// Pane A opens before it has a session. The next resolution must see it
	// immediately, even though its shell cwd does not match /repo.
	open = append(open, herdrcli.RawPaneListEntry{
		PaneID: "w1:a", Agent: "kilo", Cwd: "/launch/a", ForegroundCwd: "/repo",
	})
	if got := kilo.BackendForKilo(nil, &cwd); got != "" {
		t.Fatalf("pane A borrowed pane B's backend %q", got)
	}
}
