package pr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

func TestPublicationWritesUseEngine(t *testing.T) {
	// All commands share one migrated store and socket. Each subtest checks
	// one request without paying for unrelated engine migrations again.
	storetest.Isolate(t)
	const key = "github.com/owner/repo#1"
	s := watch.New()
	raw, _ := json.Marshal(watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, Host: "github.com", Repo: "owner/repo", Number: 1, RoomID: "publication-room", Head: "head"}}})
	saveState(t, s.Store.Root, raw)
	called := make(chan string, 1)
	sentinel := errors.New("engine handled publication")
	check := func(name, room, got string) error {
		called <- name
		if room != "publication-room" || got != key {
			t.Errorf("engine target = %q, %q", room, got)
		}
		return sentinel
	}
	calls, err := websvc.Bind([]websvc.Binding{
		{Name: "CommentPR", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, room string, ref watch.Ref, body string, publish bool) error {
			if body != "feedback" || !publish {
				t.Errorf("comment = %q, %v", body, publish)
			}
			return check("comment", room, ref.Key())
		}},
		{Name: "PostPRReview", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, room string, ref watch.Ref, body, head string, comments []watch.ReviewComment, publish bool) (int64, error) {
			if body != "feedback" || head != "head" || !publish {
				t.Errorf("review = %q, %q, %v", body, head, publish)
			}
			return 0, check("review", room, ref.Key())
		}},
		{Name: "LearnPRNoise", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, room, got, comment, reason string) (struct{}, error) {
			if comment != "issue-comments:12" || reason != "spam" {
				t.Errorf("noise = %q, %q", comment, reason)
			}
			return struct{}{}, check("noise", room, got)
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), calls)
	args := map[string][]string{
		"comment": {"comment", "https://github.com/owner/repo/pull/1", "feedback", "--publish"},
		"review":  {"review", "https://github.com/owner/repo/pull/1", "--body-file", "-", "--head", "head", "--publish"},
		"noise":   {"noise", "https://github.com/owner/repo/pull/1", "issue-comments:12", "--reason", "spam"},
	}
	for _, name := range []string{"comment", "review", "noise"} {
		t.Run(name, func(t *testing.T) {
			cmd := Command(Options{Service: func() *watch.Service { return s }})
			cmd.SetIn(strings.NewReader("feedback"))
			cmd.SetArgs(args[name])
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
				t.Fatalf("engine error = %v", err)
			}
			select {
			case got := <-called:
				if got != name {
					t.Fatalf("engine call = %q, want %q", got, name)
				}
			default:
				t.Fatal("command bypassed the engine")
			}
		})
	}
}
