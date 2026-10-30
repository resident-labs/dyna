package notify

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestShellSilenceWakesRootOnce(t *testing.T) {
	f := newFixture(t)
	id := "sh_20261023-120000-0000000a"
	got, err := f.svc.ShellSilence(f.ctx, f.a.ID, id, "build", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Message.OwnerReport || !wakesRoot(got.Message, f.a.Name) || !strings.Contains(got.Message.Text, "does not mean the job is stuck") {
		t.Fatalf("silence notice = %+v", got.Message)
	}
	again, err := f.svc.ShellSilence(f.ctx, f.a.ID, id, "build", "", 30)
	if err != nil || again.Message.ID != "" {
		t.Fatalf("repeat = %+v, %v", again.Message, err)
	}
}

func TestShellSilenceWakesStartingSession(t *testing.T) {
	f := newFixture(t)
	got, err := f.svc.ShellSilence(f.ctx, f.a.ID, "sh_20261023-120000-0000000b", "build", f.decoder.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.OwnerReport || wakesRoot(got.Message, f.a.Name) || !reflect.DeepEqual(got.Message.To, []string{f.decoder.Address()}) || !strings.Contains(got.Message.Text, "started by decoder") {
		t.Fatalf("silence notice = %+v", got.Message)
	}
}

func TestShellInactivityDeduplicatesEachQuietPeriod(t *testing.T) {
	f := newFixture(t)
	id := "sh_20261023-120000-0000000a"
	first, err := f.svc.ShellInactivity(f.ctx, f.a.ID, id, "build", "", 30, time.Time{})
	if err != nil || !wakesRoot(first.Message, f.a.Name) || !strings.Contains(first.Message.Text, "CPU activity is not measured") {
		t.Fatalf("first = %+v, %v", first.Message, err)
	}
	again, err := f.svc.ShellInactivity(f.ctx, f.a.ID, id, "build", "", 30, time.Time{})
	if err != nil || again.Message.ID != "" {
		t.Fatalf("repeat = %+v, %v", again.Message, err)
	}
	next, err := f.svc.ShellInactivity(f.ctx, f.a.ID, id, "build", "", 30, time.Unix(123, 0))
	if err != nil || next.Message.ID == "" || next.Message.ID == first.Message.ID {
		t.Fatalf("next = %+v, %v", next.Message, err)
	}
}
