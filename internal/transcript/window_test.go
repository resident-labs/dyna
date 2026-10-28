package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestSessionWindowBounds(t *testing.T) {
	store := &subagent.Store{Root: t.TempDir()}
	id := "ses_20261017-000001-aaaaaaaa"
	dir, _ := store.Dir(id)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	recordtest.Write(t, store.Root, id, subagent.Meta{ID: id, Name: "lead", Room: "global", RoomID: "global", Turns: 3, Created: time.Now()})
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for n := 1; n <= 3; n++ {
		write(filepath.Join(dir, "turns", strconv.Itoa(n)+".json"), subagent.Turn{N: n, Status: subagent.TurnOK, Started: base.Add(time.Duration(n) * time.Minute), Ended: base.Add(time.Duration(n)*time.Minute + 30*time.Second)})
	}
	loader := NewLoader(store)
	for _, count := range []int{0, 3, 6, 2} {
		got, err := loader.Load(id, count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 2 {
			if !got.Omitted || !got.Since.Equal(base.Add(90*time.Second)) {
				t.Fatalf("trimmed window: %+v", got)
			}
		} else if got.Omitted || !got.Since.IsZero() {
			t.Fatalf("untrimmed bound: %+v", got)
		}
	}
	for n := 1; n <= 3; n++ {
		got, err := loader.LoadTurn(id, n)
		if err != nil {
			t.Fatal(err)
		}
		want := time.Time{}
		if n > 1 {
			want = base.Add(time.Duration(n-1)*time.Minute + 30*time.Second)
		}
		if !got.Since.Equal(want) || !got.Before.Equal(base.Add(time.Duration(n)*time.Minute+30*time.Second)) {
			t.Fatalf("focused %d: %+v", n, got)
		}
	}
	latest, err := loader.Load(id, 2)
	if err != nil || len(latest.Turns) != 2 || latest.Info.Turns != 3 || latest.Turns[0].N != 2 || latest.Turns[1].N != 3 {
		t.Fatalf("latest page = %+v, %v", latest, err)
	}
	for _, tc := range []struct {
		name    string
		load    func() (Session, error)
		omitted bool
		newer   bool
	}{
		{"before first", func() (Session, error) { return loader.LoadPage(id, 1, 3) }, false, true},
		{"after last", func() (Session, error) { return loader.LoadAfter(id, 3, 3) }, true, false},
	} {
		got, err := tc.load()
		if err != nil || len(got.Turns) != 0 || got.Omitted != tc.omitted || got.Newer != tc.newer {
			t.Fatalf("%s = %+v, %v", tc.name, got, err)
		}
	}
	if _, err := loader.LoadPage(id, 4, 3); err == nil {
		t.Fatal("accepted a cursor after the latest turn")
	}
	emptyID := "ses_20261017-000001-bbbbbbbb"
	recordtest.Write(t, store.Root, emptyID, subagent.Meta{ID: emptyID, Name: "empty", Room: "global", RoomID: "global", Created: time.Now()})
	empty, err := loader.Load(emptyID, 3)
	if err != nil || len(empty.Turns) != 0 || empty.Omitted || empty.Newer {
		t.Fatalf("empty session = %+v, %v", empty, err)
	}
}
