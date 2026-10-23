package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/cli/change"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"github.com/spf13/cobra"
)

func TestVerdictCommandsUseEngineAndPreserveNotices(t *testing.T) {
	const key = "github.com/owner/repo#17"
	for _, name := range []string{"pr", "change"} {
		t.Run(name, func(t *testing.T) {
			storetest.Isolate(t)
			s := watch.New()
			raw, err := json.Marshal(watch.State{Version: 1, Records: map[string]*watch.Record{key: {Key: key, RoomID: "review-room", Head: "head"}}})
			if err != nil {
				t.Fatal(err)
			}
			saveState(t, s.Store.Root, raw)
			called := false
			calls, err := websvc.Bind([]websvc.Binding{{Name: "RecordReviewResult", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: func(ctx context.Context, room, keyArg string, result watch.ReviewResult) (struct{ Notice string }, error) {
				called = true
				if room != "review-room" || keyArg != key || result.Head != "head" || result.Verdict != "approve" || result.Summary != "Review complete." || result.ReviewID != 42 || len(result.Findings) != 0 {
					t.Errorf("verdict arguments: %q %q %+v", room, keyArg, result)
				}
				return struct{ Notice string }{Notice: "verdict saved; applies after GitHub refresh"}, nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			enginetest.Serve(t, store.DataDir(), calls)
			var cmd *cobra.Command
			ref := key
			if name == "pr" {
				cmd = Command(Options{Service: func() *watch.Service { return s }})
				ref = "https://github.com/owner/repo/pull/17"
			} else {
				cmd = change.Command(change.Options{Service: func() *watch.Service { return s }})
			}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"verdict", ref, "approve", "Review complete.", "--head", "head", "--review-id", "42"})
			if err := cmd.Execute(); err != nil || !called || strings.TrimSpace(out.String()) != "verdict saved; applies after GitHub refresh" {
				t.Fatalf("engine called=%v, output=%q, error=%v", called, out.String(), err)
			}
		})
	}
}
