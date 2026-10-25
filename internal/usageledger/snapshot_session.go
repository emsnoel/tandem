package usageledger

import (
	"sort"
	"strings"
)

// unknownOwner stands for a claimant that cannot be named: a source whose log
// is another session than the one registered.
const unknownOwner = ""

type span struct {
	start, end int
	owner      string
}

// collectSession attributes one provider session's events and decides the
// finality of the collaboration's sources on it.
func (snap *Snapshot) collectSession(collab, key string, sources []SnapshotSource, log SessionLog, partial map[string]bool) {
	provider := sources[0].Provider
	if log.SessionID == "" || log.Unavailable != "" {
		why := log.Unavailable
		if why == "" {
			why = "no log was found"
		}
		for _, src := range sources {
			snap.addSource(src, key, []string{"missing_source:" + src.ID})
		}
		snap.Reasons = append(snap.Reasons, "source_unavailable:"+key+":"+why)
		partial[provider] = true
		return
	}
	segmented := false
	for _, src := range sources {
		segmented = segmented || src.Segment != nil
	}
	var logReasons []string
	if log.Problem != "" {
		logReasons = append(logReasons, log.Problem+":"+key)
	}
	for _, r := range log.Cursor.View(log.Held) {
		switch {
		case strings.HasPrefix(r, "truncated_tail:"):
			snap.PendingTails = append(snap.PendingTails, Tail{SourceKey: key, Reason: r})
			snap.Reasons = append(snap.Reasons, "pending_tail:"+key)
		case segmented && r == "no_terminal_event":
			// A long-lived planner's session goes on; its segment closes instead.
		default:
			logReasons = append(logReasons, "source_incomplete:"+key+":"+r)
		}
	}
	events := log.Cursor.Settle(log.Events)
	pos := map[string]int{}
	mismatch := false
	for i, e := range events {
		pos[e.EventID] = i
		mismatch = mismatch || (e.SessionID != nil && *e.SessionID != log.SessionID)
	}
	var spans []span
	for _, g := range log.Segments {
		start, end := 0, len(events)-1
		if i, ok := pos[g.StartAfter]; ok && g.StartAfter != "" {
			start = i + 1
		}
		if i, ok := pos[g.EndThrough]; ok && g.EndThrough != "" {
			end = i
		}
		spans = append(spans, span{start: start, end: end, owner: g.CollaborationID})
	}
	claimsWhole := false
	for _, src := range sources {
		why := append([]string{}, logReasons...)
		switch {
		case mismatch:
			why = append(why, "session_mismatch:"+src.ID)
		case src.Segment != nil:
			why = append(why, segmentReasons(src, events, pos)...)
		default:
			claimsWhole = true
			if !src.Settled {
				why = append(why, "no_terminal_evidence:"+src.ID)
			}
		}
		snap.addSource(src, key, why)
		if len(why) > 0 {
			partial[provider] = true
		}
	}
	outside := 0
	for i, e := range events {
		claimants := map[string]bool{}
		covering := 0
		for _, sp := range spans {
			if sp.start <= i && i <= sp.end {
				claimants[sp.owner] = true
				covering++
			}
		}
		if mismatch {
			claimants[unknownOwner] = true
		} else if claimsWhole {
			claimants[collab] = true
		}
		if covering > 1 {
			snap.Reasons = append(snap.Reasons, "overlapping_ownership:"+key)
		}
		switch {
		case len(claimants) == 0:
			outside++
		case len(claimants) == 1 && claimants[collab]:
			snap.Events = append(snap.Events, OwnedEvent{Event: e, SourceKey: key})
		case !claimants[collab] && !claimants[unknownOwner]:
			// Another collaboration's event; nothing to do with this one.
		default:
			owners := []string{}
			for o := range claimants {
				if o != unknownOwner {
					owners = append(owners, o)
				}
			}
			sort.Strings(owners)
			snap.AmbiguousEvents = append(snap.AmbiguousEvents, AmbiguousEvent{Event: e, SourceKey: key, Owners: owners})
		}
	}
	snap.SourceBoundaries[key] = Boundary{Offset: log.Cursor.Offset, Line: log.Cursor.Line, PrefixHash: log.Cursor.PrefixHash, OutsideSegments: outside}
}

// segmentReasons decides whether a planner's segment is final: closed at a
// message that exists and has terminal evidence.
func segmentReasons(src SnapshotSource, events []Event, pos map[string]int) []string {
	g := src.Segment
	var why []string
	if g.StartAfter != "" {
		if _, ok := pos[g.StartAfter]; !ok {
			why = append(why, "boundary_not_found:"+src.ID+":"+g.StartAfter)
		}
	}
	switch i, ok := pos[g.EndThrough]; {
	case !g.Closed || g.EndThrough == "":
		why = append(why, "segment_open:"+src.ID)
	case !ok:
		why = append(why, "boundary_not_found:"+src.ID+":"+g.EndThrough)
	case !events[i].TerminalEvidence:
		why = append(why, "no_terminal_evidence:"+src.ID)
	}
	return why
}

// total sums owned events by provider. A counter is nil when any event's is.
func (snap *Snapshot) total(partial map[string]bool) {
	for _, e := range snap.Events {
		t, ok := snap.Totals[e.Provider]
		if !ok {
			t = Totals{InputTotal: ptr(0), CacheRead: ptr(0), CacheWrite: ptr(0), Output: ptr(0), ReasoningOutput: ptr(0)}
		}
		for _, pair := range []struct {
			sum **int64
			add *int64
		}{{&t.InputTotal, e.InputTotal}, {&t.CacheRead, e.CacheRead}, {&t.CacheWrite, e.CacheWrite},
			{&t.Output, e.Output}, {&t.ReasoningOutput, e.ReasoningOutput}} {
			if *pair.sum == nil || pair.add == nil {
				*pair.sum = nil
			} else {
				*pair.sum = ptr(**pair.sum + *pair.add)
			}
		}
		t.Partial = partial[e.Provider]
		snap.Totals[e.Provider] = t
	}
}
