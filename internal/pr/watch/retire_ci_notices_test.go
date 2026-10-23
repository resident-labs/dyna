package watch

import (
	"strings"
	"testing"
)

// An undelivered CI notice must not reach the author after the change ended.
// Tonight #208 got a "CI failed" notice 2.5 hours after its merge.
func TestEndedPRRetiresUndeliveredCINotices(t *testing.T) {
	r := &Record{Key: "github.com/o/r#1", URL: "https://github.com/o/r/pull/1", Head: "abc"}
	notice(r, "author", "CI failed (1/3): test. Fix the failure and push to this PR.", "ci:1")
	notice(r, "author", "CI for https://github.com/o/r/pull/1 has run 2 minutes; the budget is 3 minutes.", "ci-budget:1")
	notice(r, "author", "CI uploaded no JUnit XML test report.", "no-junit-report")
	notice(r, "author", "GitHub comment from owner (comment 1):\nCI failed (2/3) is quoted here.", "batch:1")
	notice(r, "author", "CI failed (2/3): test. Fix the failure and push to this PR.", "ci:2")
	r.Notices[4].Done = true

	retireCINotices(r, "change-ended")

	for i, want := range []string{"change-ended", "change-ended", "change-ended", "", ""} {
		if got := r.Notices[i].SupersededBy; got != want {
			t.Errorf("notice %d SupersededBy = %q, want %q (%q)", i, got, want, r.Notices[i].Text)
		}
	}
}

// Notices stored before Kind existed: only a pure CI notice is retired; a
// batch that also carries a GitHub comment is kept.
func TestLegacyCINoticeMatchesOnlyPureCIBatches(t *testing.T) {
	r := &Record{Key: "github.com/o/r#2", URL: "https://github.com/o/r/pull/2", Head: "abc"}
	notice(r, "author", "CI failed (1/3): test. Fix the failure and push to this PR.", "legacy:1")
	notice(r, "author", "CI failed (1/3): test. Fix the failure and push to this PR.\n\nGitHub comment from owner (comment 9):\nkeep the flag", "legacy:2")
	notice(r, "author", "GitHub comment from owner (comment 10):\nlooks good\n\nCI failed (3/3) was fixed", "legacy:3")
	notice(r, "author", "GitHub comment from owner (comment 11):\nIt failed CI three times in a row. Is that expected?", "legacy:4")
	notice(r, "lead", "PR #2 failed CI three times in a row. CI wakes to the author are paused.", "legacy:5")
	for i := range r.Notices {
		r.Notices[i].Kind = ""
	}
	retireCINotices(r, "change-ended")
	for i, want := range []string{"change-ended", "", "", "", "change-ended"} {
		if got := r.Notices[i].SupersededBy; got != want {
			t.Errorf("legacy notice %d SupersededBy = %q, want %q", i, got, want)
		}
	}
}

// Every path that ends a change saves through Store.update, so landing,
// closing and refreshes all retire pending CI notices.
func TestStoreUpdateRetiresCINoticesForEndedChanges(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	if err := store.update(func(st *State) error {
		open := &Record{Key: "open", URL: "https://github.com/o/r/pull/3", Head: "abc", State: "open", Watching: true}
		landed := &Record{Key: "landed", URL: "https://github.com/o/r/pull/4", Head: "abc", State: "open", Watching: true}
		notice(open, "author", "CI failed (1/3): test.", "ci:open")
		notice(landed, "author", "CI failed (1/3): test.", "ci:landed")
		st.Records = map[string]*Record{"open": open, "landed": landed}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Same transition as dyna git pr land: merged and no longer watched.
	if err := store.update(func(st *State) error {
		st.Records["landed"].State = "merged"
		st.Records["landed"].Watching = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Records["landed"].Notices[0].SupersededBy; got != "change-ended" {
		t.Fatalf("landed change CI notice SupersededBy = %q, want change-ended", got)
	}
	if got := st.Records["open"].Notices[0].SupersededBy; got != "" {
		t.Fatalf("open change CI notice SupersededBy = %q, want empty", got)
	}
}

// A poll's notice is kind "ci" only when it holds no comment, so retiring CI
// notices never drops a comment batched with a CI result.
func TestPollNoticeKindKeepsBatchedComments(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.track()
	e.setCI("failure", 1)
	e.tick()
	ci := lastNotice(t, e.record())
	if ci.Kind != "ci" {
		t.Fatalf("CI-only notice kind = %q, want ci: %q", ci.Kind, ci.Text)
	}
	e.addTrustedComment(201, "issue-comments", "Please keep the old flag.", "owner")
	// GitHub bumps the pull's updated_at for a new comment; the cheap
	// unchanged poll relies on that to fetch it.
	e.f.Pull.Updated = "2026-10-18T09:00:00Z"
	e.setCI("failure", 2)
	e.tick()
	mixed := lastNotice(t, e.record())
	if mixed.Kind != "" || !strings.Contains(mixed.Text, "Please keep the old flag.") || !strings.Contains(mixed.Text, "CI failed (2/3)") {
		t.Fatalf("mixed notice kind %q text %q, want kind empty with comment and CI result", mixed.Kind, mixed.Text)
	}
}

func lastNotice(t *testing.T, r Record) Notice {
	t.Helper()
	if len(r.Notices) == 0 {
		t.Fatal("no notices")
	}
	return r.Notices[len(r.Notices)-1]
}
