package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewCommentsUseCompleteSnapshot(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	inline := func(id int64, commit string) Comment {
		return Comment{ID: id, Kind: "comments", User: User{Login: "new-reviewer[bot]", Type: "Bot"}, Commit: commit, Path: "parser.go", Line: 12, Body: "Check bounds", URL: "https://github.com/owner/repo/pull/17#discussion_r1"}
	}
	keep := inline(1, r.Head)
	stale := inline(2, "old-head")
	dropped := inline(3, r.Head)
	human := inline(4, r.Head)
	human.User = User{Login: "person"}
	outdated := inline(5, r.Head)
	outdated.Line, outdated.OriginalLine = 0, 12
	missingPath := inline(6, r.Head)
	missingPath.Path = ""
	second := inline(7, r.Head)
	r.CommentDecisions = map[string]Decision{"comments:3": {Action: "drop", Reason: "test"}}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	firstPath := "repos/owner/repo/pulls/17/comments?per_page=100"
	nextPath := firstPath + "&page=2"
	page1, _ := json.Marshal([]Comment{keep, stale, dropped, human, outdated, missingPath})
	page2, _ := json.Marshal([]Comment{second})
	st.Responses = map[string]map[string]cachedResponse{r.Key: {
		"github.com/" + firstPath:                                     {Body: page1, Next: nextPath},
		"github.com/" + nextPath:                                      {Body: page2},
		"github.com/repos/owner/repo/pulls/17/reviews?per_page=100":   {Body: json.RawMessage(`[]`)},
		"github.com/repos/owner/repo/issues/17/comments?per_page=100": {Body: json.RawMessage(`[]`)},
	}}
	// A complete snapshot must work even when GitHub is unavailable.
	e.s.GH.Executable = filepath.Join(t.TempDir(), "no-gh")
	comments, err := e.s.reviewComments(context.Background(), st, r)
	if err != nil {
		t.Fatal(err)
	}
	// Only known noise is left out: human, old-head and outdated comments stay.
	ids := []int64{}
	for _, c := range comments {
		ids = append(ids, c.ID)
	}
	if fmt.Sprint(ids) != "[1 2 4 5 6 7]" || comments[0].Line != 12 || comments[0].Path != "parser.go" || comments[0].Commit != r.Head || comments[0].URL != keep.URL {
		t.Fatalf("review comments: %+v", comments)
	}
}

func TestReviewCommentsFetchIncompleteSnapshot(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	e.f.Comments = []Comment{{ID: 8, Kind: "comments", User: User{Login: "reviewer[bot]"}, Commit: r.Head, Path: "parser.go", Line: 4, Body: "Bug: check bounds"}, {ID: 9, Kind: "reviews", User: User{Login: "reviewer[bot]"}, Commit: r.Head, Body: "Summary"}}
	e.write()
	st, _ := e.s.Store.Read()
	firstPath := "github.com/repos/owner/repo/pulls/17/comments?per_page=100"
	st.Responses = map[string]map[string]cachedResponse{r.Key: {firstPath: {Body: json.RawMessage(`[]`), Next: "repos/owner/repo/pulls/17/comments?per_page=100&page=2"}}}
	comments, err := e.s.reviewComments(context.Background(), st, r)
	if err != nil || len(comments) != 2 || comments[0].ID != 9 || comments[1].ID != 8 {
		t.Fatalf("fetch: %+v, %v", comments, err)
	}
}

func TestLocalReviewTaskDoesNotRequireGitHub(t *testing.T) {
	e := newEnv(t)
	r := Record{Kind: "local", Key: "local-test", Head: "assigned-head", Repo: "local", Round: 1}
	e.s.GH.Executable = filepath.Join(t.TempDir(), "no-gh")
	task, err := e.s.prepareReviewTask(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, "Confirm or reject every bot finding with a short reason") {
		t.Fatalf("missing bot review instruction: %s", task)
	}
	paths, err := filepath.Glob(filepath.Join(e.s.Store.Root, "review-inputs", "*", "*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("context paths: %+v, %v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var input ReviewContext
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if input.Comments == nil || len(input.Comments) != 0 {
		t.Fatalf("local comments: %+v", input.Comments)
	}
}

func TestReviewTaskWritesKeptComments(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	comment := Comment{ID: 11, User: User{Login: "reviewer[bot]"}, Commit: r.Head, Path: "parser.go", Line: 5, Body: "Bug: validate input"}
	body, _ := json.Marshal([]Comment{comment})
	err := e.s.Store.update(func(st *State) error {
		if st.Responses == nil {
			st.Responses = map[string]map[string]cachedResponse{}
		}
		st.Responses[r.Key] = map[string]cachedResponse{
			"github.com/repos/owner/repo/pulls/17/comments?per_page=100":  {Body: body},
			"github.com/repos/owner/repo/pulls/17/reviews?per_page=100":   {Body: json.RawMessage(`[]`)},
			"github.com/repos/owner/repo/issues/17/comments?per_page=100": {Body: json.RawMessage(`[]`)},
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.s.GH.Executable = filepath.Join(t.TempDir(), "no-gh")
	task, err := e.s.prepareReviewTask(context.Background(), r, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(task, "Confirm or reject every bot finding with a short reason") {
		t.Fatal("missing bot instruction")
	}
	paths, _ := filepath.Glob(filepath.Join(e.s.Store.Root, "review-inputs", "*", "*.json"))
	if len(paths) != 1 {
		t.Fatalf("context paths: %+v", paths)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var input ReviewContext
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Comments) != 1 || input.Comments[0].ID != 11 || input.Comments[0].Commit != r.Head {
		t.Fatalf("saved comment context: %+v", input.Comments)
	}
}
