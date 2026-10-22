package engineclient_test

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/websvc"
)

// serveBuild serves calls as an engine of build engine.
func serveBuild(t *testing.T, engine string) {
	t.Helper()
	calls, err := websvc.Bind([]websvc.Binding{{Name: "Echo", Access: websvc.Read, Scope: websvc.ScopeRead, Method: func(s string) string { return s }}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.ServeAs(t, t.TempDir(), engine, calls)
}

// A method the engine does not have, asked by another build, fails with one
// line that names both builds.
func TestUnknownMethodAcrossBuildsNamesBoth(t *testing.T) {
	serveBuild(t, "v1")
	err := engineclient.New("", engineclient.Build("v2"), engineclient.Notices(nil)).Call(t.Context(), "NewInV2", nil, nil)
	want := "engine is v1, this dyna is v2: restart the engine or update dyna (the engine has no method NewInV2)"
	if err == nil || err.Error() != want {
		t.Fatalf("unknown method = %v, want %q", err, want)
	}
}

// A gap alone does not refuse a call; the client says so once.
func TestKnownMethodAcrossBuildsWarnsOnce(t *testing.T) {
	serveBuild(t, "v1")
	var out bytes.Buffer
	client := engineclient.New("", engineclient.Build("v2"), engineclient.Notices(&out))
	for range 2 {
		var reply string
		if err := client.Call(t.Context(), "Echo", []any{"hi"}, &reply); err != nil || reply != "hi" {
			t.Fatalf("Echo = %q, %v", reply, err)
		}
	}
	if got := out.String(); got != "dyna: engine is v1, this dyna is v2: restart the engine or update dyna\n" {
		t.Fatalf("warning = %q, want one line", got)
	}
}

// The same build says nothing.
func TestSameBuildIsQuiet(t *testing.T) {
	serveBuild(t, "v1")
	var out bytes.Buffer
	if err := engineclient.New("", engineclient.Build("v1"), engineclient.Notices(&out)).Call(t.Context(), "Echo", []any{"hi"}, nil); err != nil || strings.TrimSpace(out.String()) != "" {
		t.Fatalf("same build: %v, %q", err, out.String())
	}
}
