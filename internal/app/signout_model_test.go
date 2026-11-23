package app

import (
	"fmt"
	"strings"
	"testing"
)

// An exhaustive model of sign-out against the processes that use a profile meanwhile. Each
// design lists the steps its processes take, in the order the code takes them; the explorer
// runs every interleaving (and, for logout, a crash before any step) and checks:
//
//   - blocked: once logout starts, the grant is never usable, so it holds no tokens or the
//     sign-out marker is set (MAR-552, #7);
//   - quiet: once a logout has cleared the profile and every hook has finished, no reported
//     call is left in the spool for the next sign-in to upload (MAR-1872, MAR-1881).
//
// Designs that shipped with a bug must fail with a counterexample, and the current one must
// pass. A change to SignOutMcp's or ReportHook's order belongs here first. Hooks are assumed
// to run to completion: one killed between its write and its second check can still leave
// its call behind.

type signOutState struct {
	tokens, marker      bool
	logout              int // next logout step; len(steps) once finished
	started, retained   bool
	crashed             bool
	hook                [2]int  // next step of each concurrent report hook
	spooled             [2]bool // that hook's call is in the spool
	revocationConfirmed bool
}

type signOutStep struct {
	name string
	// apply returns the successor states, one per outcome the step can have.
	apply func(s signOutState, hook int) []signOutState
}

type signOutDesign struct {
	name   string
	logout []signOutStep
	hook   []signOutStep
	// broken names the invariant the design violates ("" for the current one).
	broken string
}

func step(name string, apply func(s *signOutState, hook int) bool) signOutStep {
	return signOutStep{name, func(s signOutState, hook int) []signOutState {
		if !apply(&s, hook) {
			s.endHook(hook)
		}
		return []signOutState{s}
	}}
}

// endHook moves a hook past its last step (hook -1 is logout, which ends by stopping).
func (s *signOutState) endHook(hook int) {
	if hook >= 0 {
		s.hook[hook] = 1 << 10
	}
}

var (
	// Logout steps (SignOutMcp, refusedLogout.clear).
	markSignOut = step("logout: mark sign-out unfinished", func(s *signOutState, _ int) bool { s.marker = true; return true })
	// The server confirms the revocation or does not (timeout, 5xx, captive portal).
	revoke = signOutStep{"logout: revoke the grant", func(s signOutState, _ int) []signOutState {
		confirmed := s
		confirmed.revocationConfirmed = true
		return []signOutState{confirmed, s}
	}}
	keepUnconfirmed = step("logout: keep the grant when revocation is unconfirmed", func(s *signOutState, _ int) bool {
		if !s.revocationConfirmed {
			s.retained = true
			s.logout = 1 << 10
		}
		return true
	})
	markIfUnconfirmed = step("logout: mark sign-out unfinished if revocation is unconfirmed", func(s *signOutState, _ int) bool {
		if !s.revocationConfirmed {
			s.marker, s.retained = true, true
			s.logout = 1 << 10
		}
		return true
	})
	invalidate  = step("logout: delete the tokens", func(s *signOutState, _ int) bool { s.tokens = false; return true })
	clearMarker = step("logout: clear the sign-out marker", func(s *signOutState, _ int) bool { s.marker = false; return true })
	removeSpool = step("logout: remove the spool", func(s *signOutState, _ int) bool { s.spooled = [2]bool{}; return true })

	// Report hook steps (ReportHook).
	checkMarker = step("hook: profile saved, no unfinished sign-out", func(s *signOutState, _ int) bool { return !s.marker })
	checkTokens = step("hook: store confirms the tokens", func(s *signOutState, _ int) bool { return s.tokens })
	writeCall   = step("hook: write the call to the spool", func(s *signOutState, h int) bool { s.spooled[h] = true; return true })
	recheck     = step("hook: check again; remove the call if signed out", func(s *signOutState, h int) bool {
		if s.marker || !s.tokens {
			s.spooled[h] = false
		}
		return true
	})
	recheckMarker = step("hook: check the marker again; remove the call if set", func(s *signOutState, h int) bool {
		if s.marker {
			s.spooled[h] = false
		}
		return true
	})
)

var signOutDesigns = []signOutDesign{
	{
		name:   "#7 before f2ba4d9: the marker is written only after an unconfirmed revocation",
		logout: []signOutStep{revoke, markIfUnconfirmed, invalidate},
		broken: "blocked",
	},
	{
		name:   "#27 (09d8325): the hook spools whatever the sign-in state, logout keeps the spool",
		logout: []signOutStep{markSignOut, revoke, keepUnconfirmed, invalidate, clearMarker},
		hook:   []signOutStep{writeCall},
		broken: "quiet",
	},
	{
		name:   "#29 at 4702b9a6: the hook checks only the profile file and the marker",
		logout: []signOutStep{markSignOut, revoke, keepUnconfirmed, invalidate, clearMarker, removeSpool},
		hook:   []signOutStep{checkMarker, writeCall},
		broken: "quiet",
	},
	{
		name:   "#29 merged (14fec7d): the hook checks the tokens before it writes, never after (MAR-1881)",
		logout: []signOutStep{markSignOut, revoke, keepUnconfirmed, invalidate, clearMarker, removeSpool},
		hook:   []signOutStep{checkMarker, checkTokens, writeCall},
		broken: "quiet",
	},
	// What the current design depends on: SignOutMcp clears the marker only once the tokens
	// are gone, and the hook's second check reads the tokens, not only the marker.
	{
		name:   "mutation: logout clears the marker before deleting the tokens",
		logout: []signOutStep{markSignOut, revoke, keepUnconfirmed, clearMarker, invalidate, removeSpool},
		hook:   []signOutStep{checkMarker, checkTokens, writeCall, recheck},
		broken: "blocked",
	},
	{
		name:   "mutation: the hook checks only the marker again",
		logout: []signOutStep{markSignOut, revoke, keepUnconfirmed, invalidate, clearMarker, removeSpool},
		hook:   []signOutStep{checkMarker, checkTokens, writeCall, recheckMarker},
		broken: "quiet",
	},
	{
		name:   "current: SignOutMcp and ReportHook",
		logout: []signOutStep{markSignOut, revoke, keepUnconfirmed, invalidate, clearMarker, removeSpool},
		hook:   []signOutStep{checkMarker, checkTokens, writeCall, recheck},
	},
}

// violation returns the invariant s breaks, if any; final is whether every process is done.
func (d signOutDesign) violation(s signOutState, final bool) string {
	if s.started && s.tokens && !s.marker {
		return "blocked"
	}
	if final && !s.crashed && !s.retained && s.logout >= len(d.logout) && (s.spooled[0] || s.spooled[1]) {
		return "quiet"
	}
	return ""
}

type signOutTrace struct {
	state signOutState
	steps []string
}

// explore searches every interleaving breadth first and returns the shortest one that breaks
// an invariant, and the number of states seen.
func (d signOutDesign) explore() (string, []string, int) {
	hooks := 0
	if len(d.hook) > 0 {
		hooks = 2
	}
	start := signOutState{tokens: true}
	for h := hooks; h < 2; h++ {
		start.endHook(h)
	}
	seen := map[signOutState]bool{start: true}
	queue := []signOutTrace{{state: start}}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		var next []signOutTrace
		add := func(name string, states ...signOutState) {
			for _, s := range states {
				next = append(next, signOutTrace{s, append(append([]string(nil), current.steps...), name)})
			}
		}
		s := current.state
		if s.logout < len(d.logout) && !s.crashed {
			after := d.logout[s.logout].apply(s, -1)
			for i := range after {
				after[i].started = true
				if after[i].logout == s.logout {
					after[i].logout++
				}
			}
			add(d.logout[s.logout].name, after...)
			crashed := s
			crashed.crashed = true
			add("logout: interrupted", crashed)
		}
		for h := 0; h < 2; h++ {
			if s.hook[h] < len(d.hook) {
				after := d.hook[s.hook[h]].apply(s, h)
				for i := range after {
					if after[i].hook[h] == s.hook[h] {
						after[i].hook[h]++
					}
				}
				add(fmt.Sprintf("%s (hook %d)", d.hook[s.hook[h]].name, h+1), after...)
			}
		}
		if broken := d.violation(s, len(next) == 0); broken != "" {
			return broken, current.steps, len(seen)
		}
		for _, n := range next {
			if !seen[n.state] {
				seen[n.state] = true
				queue = append(queue, n)
			}
		}
	}
	return "", nil, len(seen)
}

func TestSignOutModel(t *testing.T) {
	for _, d := range signOutDesigns {
		broken, trace, states := d.explore()
		switch {
		case broken != d.broken && d.broken == "":
			t.Errorf("%s breaks %q after:\n  %s", d.name, broken, strings.Join(trace, "\n  "))
		case broken != d.broken:
			t.Errorf("%s: the model no longer finds its %q bug (found %q in %d states)", d.name, d.broken, broken, states)
		default:
			t.Logf("%s: %d states, broken %q %v", d.name, states, broken, trace)
		}
	}
}
