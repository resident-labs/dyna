package goals

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWaitPreservesProgress(t *testing.T) {
	s := testStore(t)
	g, err := s.Add("r", "owner", Input{Text: "Ship the relay", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	original := g.UpdatedAt
	now := original.Add(time.Minute)
	s.Now = func() time.Time { return now }
	until := now.Add(time.Hour)
	got, err := s.Wait("r", g.ID, "agent", until)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WaitAt.Equal(now) || !got.WaitUntil.Equal(until) || !got.UpdatedAt.Equal(original) {
		t.Fatalf("wait changed progress: %#v", got)
	}
	if len(got.Edits) != 1 || got.Edits[0].By != "agent" || !got.Edits[0].At.Equal(now) || !got.Edits[0].WaitUntil.Equal(until) || got.Edits[0].OldText != g.Text {
		t.Fatalf("wait history = %#v", got.Edits)
	}
	loaded, err := s.Get("r", g.ID)
	// JSON preserves instants, but does not preserve time.Location pointers.
	written, _ := json.Marshal(got)
	reloaded, _ := json.Marshal(loaded)
	if err != nil || string(written) != string(reloaded) {
		t.Fatal(loaded, err)
	}
	shorter := now.Add(time.Minute)
	got, err = s.Wait("r", g.ID, "owner", shorter)
	if err != nil || !got.WaitUntil.Equal(shorter) || !got.UpdatedAt.Equal(original) {
		t.Fatalf("shorter wait = %#v, %v", got, err)
	}
	if len(got.Edits) != 2 || !got.Edits[0].WaitUntil.Equal(until) || !got.Edits[1].WaitUntil.Equal(shorter) {
		t.Fatalf("replacement history = %#v", got.Edits)
	}

}

func TestWaitRejectsInvalidGoalsAndDeadlines(t *testing.T) {
	s := testStore(t)
	g, err := s.Add("r", "owner", Input{Text: "Ship the relay", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, until := range []time.Time{time.Time{}, s.now(), s.now().Add(-time.Second)} {
		if _, err := s.Wait("r", g.ID, "agent", until); err == nil {
			t.Fatalf("accepted %v", until)
		}
	}
	for _, room := range []string{"r", "other"} {
		if _, err := s.Wait(room, "missing", "agent", s.now().Add(time.Hour)); err == nil {
			t.Fatal("accepted missing goal")
		}
	}
	for _, drop := range []bool{false, true} {
		if _, err := s.Finish("r", g.ID, "owner", "Closed", drop); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Wait("r", g.ID, "agent", s.now().Add(time.Hour)); err == nil {
			t.Fatal("accepted closed goal")
		}
	}
}
