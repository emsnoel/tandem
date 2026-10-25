package usageledger

import (
	"fmt"
	"sort"
)

// Collaboration snapshots: what one collaboration's registered sources used,
// and whether that is final.
//
//   - A source without segments owns every event of its session; a planner
//     owns the events between its start and end boundaries.
//   - An event another collaboration also covers, or from an unregistered
//     session, is ambiguous: kept apart, counted for nobody, and the snapshot
//     is incomplete.
//   - A source is final when its work has ended and its log ends in terminal
//     evidence. A quiet log is not final.
//   - The snapshot is complete only when every source is final, no tail is
//     pending, nothing is ambiguous and the collaboration is closed.

// CollectorVersion names this collector in every snapshot it builds.
const CollectorVersion = "tandem-ledger/1"

// SnapshotSegment is a planner's ownership of its session.
type SnapshotSegment struct {
	CollaborationID, SourceID, StartAfter, EndThrough string
	Closed                                            bool
}

// SnapshotSource is one registered source as collection sees it.
type SnapshotSource struct {
	ID, Provider, Role, SessionID string
	// Settled: the source's work has ended (its job settled or it was marked
	// terminal). Planners are settled by closing their segment instead.
	Settled bool
	Segment *SnapshotSegment
}

// SessionLog is what collection holds for one provider session.
type SessionLog struct {
	Provider, SessionID string
	// Unavailable says why the log could not be read; empty when it was.
	Unavailable string
	// Problem names a log that shrank or disappeared after it was read. Its
	// stored events still count, as a lower bound, and the source is not final.
	Problem string
	Events  []Event
	Cursor  Cursor
	// Held is the bytes after the cursor that were not consumed.
	Held []byte
	// Segments are every collaboration's segments of this session.
	Segments []SnapshotSegment
}

// SnapshotInput is everything BuildSnapshot reads.
type SnapshotInput struct {
	CollaborationID, Status string
	Sources                 []SnapshotSource
	Logs                    []SessionLog
}

// OwnedEvent is an event charged to the collaboration.
type OwnedEvent struct {
	Event
	SourceKey string `json:"source_key"`
}

// AmbiguousEvent is an event more than one collaboration claims. Owners are
// the known claimants; an empty list means the claimant is unknown.
type AmbiguousEvent struct {
	Event
	SourceKey string   `json:"source_key"`
	Owners    []string `json:"owners"`
}

// SourceStatus says whether a source's usage is final.
type SourceStatus struct {
	ID        string   `json:"source_id"`
	Provider  string   `json:"provider"`
	Role      string   `json:"role"`
	SessionID string   `json:"session_id"`
	SourceKey string   `json:"source_key"`
	Final     bool     `json:"final"`
	Reasons   []string `json:"reasons"`
}

// Boundary is how far a session has been read.
type Boundary struct {
	Offset          int64  `json:"offset"`
	Line            int    `json:"line"`
	PrefixHash      string `json:"prefix_hash"`
	OutsideSegments int    `json:"outside_segments"`
}

// Tail is a session line still being written.
type Tail struct {
	SourceKey string `json:"source"`
	Reason    string `json:"reason"`
}

// Totals are one provider's counters; a counter is nil when any event's is.
// Partial is set when a source of the provider is not final.
type Totals struct {
	InputTotal      *int64 `json:"input_total"`
	CacheRead       *int64 `json:"cache_read"`
	CacheWrite      *int64 `json:"cache_write"`
	Output          *int64 `json:"output"`
	ReasoningOutput *int64 `json:"reasoning_output"`
	Partial         bool   `json:"partial"`
}

// Snapshot is one collaboration's usage as collected.
type Snapshot struct {
	SchemaVersion    int                 `json:"schema_version"`
	CollaborationID  string              `json:"collaboration_id"`
	CollectorVersion string              `json:"collector_version"`
	Status           string              `json:"status"`
	Complete         bool                `json:"complete"`
	Reasons          []string            `json:"reasons"`
	Events           []OwnedEvent        `json:"events"`
	AmbiguousEvents  []AmbiguousEvent    `json:"ambiguous_events"`
	ExpectedSources  []SourceStatus      `json:"expected_sources"`
	SourceBoundaries map[string]Boundary `json:"source_boundaries"`
	PendingTails     []Tail              `json:"pending_tails"`
	Totals           map[string]Totals   `json:"totals"`
}

// snapshotComplete is the final check on counts: every expected source
// terminal, no pending tail and valid ownership. It is a guard on top of the
// parser's and the ownership rules' reasons, not the whole validator.
func snapshotComplete(expected, terminal, pending int, ownershipValid bool) bool {
	return expected > 0 && expected == terminal && pending == 0 && ownershipValid
}

func sourceKey(provider, session string) string { return provider + ":" + session }

// BuildSnapshot computes a collaboration's snapshot from its sources and logs.
func BuildSnapshot(in SnapshotInput) Snapshot {
	snap := Snapshot{SchemaVersion: 2, CollaborationID: in.CollaborationID, CollectorVersion: CollectorVersion, Status: in.Status,
		Reasons: []string{}, Events: []OwnedEvent{}, AmbiguousEvents: []AmbiguousEvent{}, ExpectedSources: []SourceStatus{},
		SourceBoundaries: map[string]Boundary{}, PendingTails: []Tail{}, Totals: map[string]Totals{}}
	logs := map[string]SessionLog{}
	for _, l := range in.Logs {
		logs[sourceKey(l.Provider, l.SessionID)] = l
	}
	bySession := map[string][]SnapshotSource{}
	var keys []string
	for _, src := range in.Sources {
		if src.SessionID == "" {
			snap.addSource(src, "", []string{"unresolved_identity:" + src.ID})
			continue
		}
		k := sourceKey(src.Provider, src.SessionID)
		if _, seen := bySession[k]; !seen {
			keys = append(keys, k)
		}
		bySession[k] = append(bySession[k], src)
	}
	sort.Strings(keys)
	partial := map[string]bool{}
	for _, k := range keys {
		snap.collectSession(in.CollaborationID, k, bySession[k], logs[k], partial)
	}
	if in.Status != "closed" {
		snap.Reasons = append(snap.Reasons, fmt.Sprintf("collaboration_%s:%s", in.Status, in.CollaborationID))
	}
	if len(snap.AmbiguousEvents) > 0 {
		snap.Reasons = append(snap.Reasons, "ambiguous_ownership")
	}
	finals := 0
	for _, s := range snap.ExpectedSources {
		if s.Final {
			finals++
		}
		snap.Reasons = append(snap.Reasons, s.Reasons...)
	}
	if len(in.Sources) == 0 {
		snap.Reasons = append(snap.Reasons, "no_expected_sources")
	}
	snap.Reasons = dedupe(snap.Reasons)
	snap.Complete = snapshotComplete(len(in.Sources), finals, len(snap.PendingTails), len(snap.AmbiguousEvents) == 0) &&
		len(snap.Reasons) == 0
	snap.total(partial)
	return snap
}

func (snap *Snapshot) addSource(src SnapshotSource, key string, why []string) {
	snap.ExpectedSources = append(snap.ExpectedSources, SourceStatus{ID: src.ID, Provider: src.Provider, Role: src.Role,
		SessionID: src.SessionID, SourceKey: key, Final: len(why) == 0, Reasons: append([]string{}, why...)})
}
