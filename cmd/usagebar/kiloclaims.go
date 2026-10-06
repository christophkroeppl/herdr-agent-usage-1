package main

import (
	"github.com/senna-lang/herdr-agent-usage/internal/herdrcli"
	"github.com/senna-lang/herdr-agent-usage/internal/providers/kilo"
)

func init() {
	kilo.ListOpenPanes = listKiloOpenPaneClaims
}

// listKiloOpenPaneClaims uses the same foreground-cwd preference as the
// sidebar. A pane may have been launched from another directory and changed
// into its project; comparing its original cwd would miss a competing pane.
// Always read the current list: cached identity evidence can attribute the
// other pane's session immediately after a new pane opens.
func listKiloOpenPaneClaims() ([]kilo.OpenPaneClaim, bool) {
	listed, ok := herdrcli.ListOpenAgentPanesOK()
	if !ok {
		return nil, false
	}
	return kiloOpenPaneClaims(listed), true
}

func kiloOpenPaneClaims(listed []herdrcli.OpenAgentPane) []kilo.OpenPaneClaim {
	panes := make([]kilo.OpenPaneClaim, 0, len(listed))
	for _, pane := range listed {
		claim := kilo.OpenPaneClaim{Cwd: deref(herdrcli.PaneSessionCwd(pane.PaneInfo))}
		if pane.Agent != nil {
			claim.Agent = *pane.Agent
		}
		if pane.AgentSession != nil && pane.AgentSession.Kind == "id" {
			claim.SessionID = pane.AgentSession.Value
		}
		panes = append(panes, claim)
	}
	return panes
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
