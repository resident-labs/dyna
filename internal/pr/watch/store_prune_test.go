package watch

import (
	"strings"
	"testing"
)

// Cached responses of merged and closed PRs were never removed. They were
// 10.6 MB of a 22.8 MB state file that every save reads and rewrites, which
// slowed every watcher tick until PR polls ran out of time.
func TestSaveDropsResponsesOfEndedPRs(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	cached := map[string]cachedResponse{"github.com/repos/o/r/pulls/1": {ETag: `"e"`}}
	if err := s.update(func(st *State) error {
		st.Records = map[string]*Record{
			"open":      {Key: "open", State: "open", Watching: true},
			"unwatched": {Key: "unwatched", State: "open"},
			"merged":    {Key: "merged", State: "merged"},
			"closed":    {Key: "closed", State: "closed"},
		}
		st.Responses = map[string]map[string]cachedResponse{"open": cached, "unwatched": cached, "merged": cached, "closed": cached}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Responses) != 2 || st.Responses["open"] == nil || st.Responses["unwatched"] == nil {
		t.Fatalf("responses after save: %v", st.Responses)
	}
}

func TestSaveDropsResponsesForPreviousHeads(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	const key = "github.com/o/r#1"
	const prefix = "github.com/repos/o/r/"
	for _, head := range []string{"new", "old"} {
		t.Run(head, func(t *testing.T) {
			if err := s.update(func(st *State) error {
				st.Records[key] = &Record{Key: key, State: "open", Head: head, Repo: "o/r", Host: "github.com"}
				if st.Responses == nil {
					st.Responses = map[string]map[string]cachedResponse{}
				}
				st.Responses[key] = map[string]cachedResponse{
					prefix + "commits/old/check-runs?per_page=100":    {},
					prefix + "commits/new/status?per_page=100":        {},
					prefix + "actions/runs?head_sha=old&per_page=100": {},
					prefix + "actions/runs?per_page=100&head_sha=new": {},
					prefix + "issues/1/comments?per_page=100":         {},
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			st, err := s.Read()
			if err != nil {
				t.Fatal(err)
			}
			entries := st.Responses[key]
			if len(entries) != 3 {
				t.Errorf("head %s kept %d responses, want current-head responses and comments: %v", head, len(entries), entries)
			}
			for _, path := range []string{prefix + "commits/" + head, prefix + "actions/runs?"} {
				found := false
				for entry := range entries {
					if strings.HasPrefix(entry, path) && (strings.Contains(entry, "/"+head+"/") || strings.Contains(entry, "head_sha="+head)) {
						found = true
					}
				}
				if !found {
					t.Errorf("lost current-head response for %s", path)
				}
			}
			if _, ok := entries[prefix+"issues/1/comments?per_page=100"]; !ok {
				t.Error("lost head-independent response")
			}
		})
	}
}
