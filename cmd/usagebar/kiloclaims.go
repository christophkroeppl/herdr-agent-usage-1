package main

import (
	"sync"
	"time"

	"github.com/senna-lang/herdr-agent-usage/internal/herdrcli"
	"github.com/senna-lang/herdr-agent-usage/internal/providers/kilo"
)

// kiloClaimTTL keeps one pane-list read across the several Kilo lookups a
// single sidebar refresh makes (context, billing, spend).
const kiloClaimTTL = 2 * time.Second

func init() {
	var (
		mu    sync.Mutex
		at    time.Time
		panes []kilo.OpenPaneClaim
		ok    bool
	)
	kilo.ListOpenPanes = func() ([]kilo.OpenPaneClaim, bool) {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && time.Since(at) < kiloClaimTTL {
			return panes, ok
		}
		listed, listedOK := herdrcli.ListOpenAgentPanesOK()
		at = time.Now()
		ok = listedOK
		panes = panes[:0]
		if !listedOK {
			return nil, false
		}
		for _, pane := range listed {
			claim := kilo.OpenPaneClaim{Cwd: deref(pane.Cwd)}
			if pane.Agent != nil {
				claim.Agent = *pane.Agent
			}
			panes = append(panes, claim)
		}
		return panes, true
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
