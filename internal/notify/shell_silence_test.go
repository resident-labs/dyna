package notify

import (
	"strings"
	"testing"
)

func TestShellSilenceWakesRootOnce(t *testing.T) {
	f := newFixture(t)
	id := "sh_20261023-120000-0000000a"
	got, err := f.svc.ShellSilence(f.ctx, f.a.ID, id, "build", 30)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Message.OwnerReport || !wakesRoot(got.Message, f.a.Name) || !strings.Contains(got.Message.Text, "does not mean the job is stuck") {
		t.Fatalf("silence notice = %+v", got.Message)
	}
	again, err := f.svc.ShellSilence(f.ctx, f.a.ID, id, "build", 30)
	if err != nil || again.Message.ID != "" {
		t.Fatalf("repeat = %+v, %v", again.Message, err)
	}
}
