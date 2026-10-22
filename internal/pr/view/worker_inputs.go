package view

import (
	"encoding/json"
	"slices"
	"strings"

	"dyna-agent/internal/pr/watch"
)

// inputPrefix marks the scan memory that holds one record's worker-input text.
const inputPrefix = "\x00input\x00"

// Tests count the actual room-wide input join used by Scan.
var scanWorkerInputs = prWorkerInputs

// prWorkerInputs includes eligibility facts, excluding timestamps and delivery receipts.
// Output-only writes update the UI without scheduling another poll of the same PR.
//
// records are the room's records. A record's text is kept in memo, which may be
// nil, and made again only when stale(key) reports it was written since.
func prWorkerInputs(st watch.State, roomID string, records []*watch.Record, memo map[string]string, stale func(key string) bool) string {
	var b strings.Builder
	for _, r := range slices.SortedFunc(slices.Values(records), func(a, b *watch.Record) int { return strings.Compare(a.Key, b.Key) }) {
		text, ok := memo[inputPrefix+r.Key]
		if !ok || stale(r.Key) {
			data, _ := json.Marshal(watch.Record{
				Head: r.Head, HeadStale: r.HeadStale, State: r.State, CI: r.CI, IsDraft: r.IsDraft, Mergeable: r.Mergeable, DefaultBranch: r.DefaultBranch, RoundHead: r.RoundHead,
				Key: r.Key, Kind: r.Kind, ChangeID: r.ChangeID, RepoPath: r.RepoPath,
				BaseRef: r.BaseRef, Author: r.Author, Dir: r.Dir, LookupDir: r.LookupDir,
				Watching: r.Watching, Notes: r.Notes, Category: r.Category,
				Dependencies: r.Dependencies, PolicyOverride: r.PolicyOverride,
				ReviewerOverride: r.ReviewerOverride, ReviewPublishing: r.ReviewPublishing,
				Verdict: r.Verdict, Summary: r.Summary, Findings: r.Findings,
			})
			text = string(data)
			if memo != nil {
				memo[inputPrefix+r.Key] = text
			}
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	cfg := st.Reviewers[roomID]
	data, _ := json.Marshal(struct {
		Reviewer watch.Reviewer
		Policy   watch.ChangePolicy
	}{watch.Reviewer{Profile: cfg.Profile, Disabled: cfg.Disabled, ReviewPublishing: cfg.ReviewPublishing}, watch.RoomPolicy(st, roomID)})
	b.Write(data)
	return b.String()
}
