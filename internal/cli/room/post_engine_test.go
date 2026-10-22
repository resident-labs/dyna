package room

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/websvc"
)

// The package's post tests run the engine's posting path in-process: the
// claim's sender and notify.Post, as the engine runs them for a verified
// caller. The tests below cover the call to the engine itself.
func init() { sendPost = postInProcess }

func postInProcess(ctx context.Context, req notify.PostRequest) (notify.Result, error) {
	svc := notify.Default()
	snd, err := svc.SenderOf(req.Claim)
	if err != nil {
		return notify.Result{}, err
	}
	return svc.Post(ctx, req.Input(snd))
}

// dyna post sends one Post call with its claim, and prints what the engine
// answers.
func TestPostGoesThroughTheEngine(t *testing.T) {
	var got []notify.PostRequest
	calls, err := websvc.Bind([]websvc.Binding{{Name: "Post", Access: websvc.Write, Scope: websvc.ScopeOperate,
		Method: func(_ context.Context, req notify.PostRequest) (notify.Result, error) {
			got = append(got, req)
			res := notify.Result{}
			res.Message.ID = "msg_0123456789abcdef"
			return res, nil
		}}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, t.TempDir(), calls)
	res, err := callEnginePost(t.Context(), notify.PostRequest{Text: "hello", Mentions: []string{"@root"}, Claim: notify.Claim{Session: "ses_a", Room: "dyna-agent"}})
	if err != nil || res.Message.ID != "msg_0123456789abcdef" {
		t.Fatalf("Post = %+v, %v", res, err)
	}
	if len(got) != 1 || got[0].Text != "hello" || got[0].Claim.Session != "ses_a" {
		t.Fatalf("engine got %+v", got)
	}
}

// With no engine, the post is not sent and dyna post exits non-zero with the
// cause (requirement 26).
func TestPostWithoutEngineFailsWithTheCause(t *testing.T) {
	saved := engineClient
	t.Cleanup(func() { engineClient = saved })
	engineClient = func() *engineclient.Client {
		return engineclient.New(filepath.Join(t.TempDir(), "engine.sock"), engineclient.RestartWait(0), engineclient.Notices(nil))
	}
	_, err := callEnginePost(t.Context(), notify.PostRequest{Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "the post was not sent") || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("post without engine: %v", err)
	}
}
