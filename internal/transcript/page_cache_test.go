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

func TestPageCacheKeepsOnlySelectedTurns(t *testing.T) {
	store := &subagent.Store{Root: t.TempDir()}
	id := "ses_20261019-000001-aaaaaaaa"
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
	recordtest.Write(t, store.Root, id, subagent.Meta{ID: id, Name: "lead", Room: "global", RoomID: "global", Turns: 9, Created: time.Now()})
	for n := 1; n <= 9; n++ {
		write(filepath.Join(dir, "turns", strconv.Itoa(n)+".json"), subagent.Turn{N: n, Status: subagent.TurnOK, Prompt: strconv.Itoa(n), Ended: time.Now()})
	}
	loader := NewLoader(store)
	for _, before := range []int{0, 7, 4, 7, 0} {
		got, err := loader.LoadPage(id, before, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Turns) != 3 {
			t.Fatalf("page length %d", len(got.Turns))
		}
		cache := loader.cache(id)
		if len(cache.done) > 3 {
			t.Fatalf("retained %d completed turns for a three-turn page", len(cache.done))
		}
		if len(got.Turns[0].Items) == 0 {
			t.Fatal("re-fetched turn lost its items")
		}
	}
	for _, after := range []int{0, 3, 6, 3, 0} {
		got, err := loader.LoadAfter(id, after, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Turns) != 3 || got.Turns[0].N != after+1 || got.Omitted != (after > 0) || got.Newer != (after < 6) {
			t.Fatalf("forward page after %d: %+v", after, got)
		}
		if len(loader.cache(id).done) > 3 {
			t.Fatal("forward page retained discarded completed turns")
		}
		if len(got.Turns[0].Items) == 0 {
			t.Fatal("forward re-fetch lost its items")
		}
	}

}
