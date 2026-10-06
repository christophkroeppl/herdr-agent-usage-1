/**
 * Open-pane claims for the cwd fallback.
 *
 * A directory with exactly one live session is not evidence that the pane asking
 * owns it: another Kilo pane in the same repository may already be that session,
 * and this pane may simply not have a session yet. The host registers the open
 * pane list; the fallback refuses to attribute when that list shows the
 * directory is shared, or when the list cannot be read.
 */
package kilo

import (
	"path/filepath"
	"strings"
)

// OpenPaneClaim is one pane currently open in Herdr.
type OpenPaneClaim struct {
	Agent string
	Cwd   string
}

// ListOpenPanes, when set, reports the panes Herdr currently has open.
//
// ok false means the list could not be read. The cwd fallback then attributes
// nothing: it cannot prove the directory belongs to this pane alone. A nil
// function means the host did not register a list, which is the fixture path
// used by tests that build a store and no Herdr session.
var ListOpenPanes func() (panes []OpenPaneClaim, ok bool)

// directoryIsShared reports whether another open Kilo pane is working in the
// same directory tree the cwd fallback would search.
//
// The current pane is part of the list, so one matching pane is this pane and
// is not a conflict. A second one is. Panes whose cwd would be reached by this
// directory's descendant query count too: a worktree checked out under the
// repository is in the same search, and attributing its session to the parent
// pane would be the same cross-attribution.
func directoryIsShared(directory string, panes []OpenPaneClaim) bool {
	matches := 0
	for _, pane := range panes {
		if !strings.EqualFold(strings.TrimSpace(pane.Agent), "kilo") {
			continue
		}
		other := normalizeDirectory(pane.Cwd)
		if other == "" || !sameDirectoryTree(directory, other) {
			continue
		}
		matches++
		if matches > 1 {
			return true
		}
	}
	return false
}

func sameDirectoryTree(a, b string) bool {
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}
