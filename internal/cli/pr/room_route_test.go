package pr

// Single-PR commands route one PR through its room, so their lookup reads
// only that PR's row. The unrelated broken row proves the scope: before the
// keyed read, one bad row anywhere failed every single-PR command.

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/enginetest"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

// prURL turns a stored PR key such as github.com/owner/repo#17 into its URL.
func prURL(key string) string {
	return "https://" + strings.Replace(key, "#", "/pull/", 1)
}

// recordBody is a minimal stored record body with its routing fields.
const recordBody = `{"record":{"roomId":"%s","host":"github.com","repo":"%s","number":%d,"state":"open"}}`

// seedRows writes PR rows straight to the store tables, so the test never
// warms the shared snapshot that a real CLI process starts without.
func seedRows(t *testing.T, rows map[string]string) {
	t.Helper()
	data := store.DataDir()
	if err := prstate.Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pr_state(id,revision,body) VALUES(1,1,'{"version":1}')`); err != nil {
		t.Fatal(err)
	}
	for key, body := range rows {
		repo, number, found := strings.Cut(strings.TrimPrefix(key, "github.com/"), "#")
		if !found {
			t.Fatalf("bad key %q", key)
		}
		n, err := strconv.Atoi(number)
		if err != nil {
			t.Fatal(err)
		}
		// The room column carries the record's room, as the write path stores it.
		if _, err = db.Exec("INSERT INTO pr_rows(key,room,repo,number,closed,rev,body) VALUES(?,coalesce(json_extract(?,'$.record.roomId'),'rm-x'),?,?,0,1,?)",
			key, body, repo, n, body); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSinglePRCommandsReadOnlyTheirRow(t *testing.T) {
	storetest.Isolate(t)
	seedRows(t, map[string]string{
		"github.com/alpha/one#11": fmt.Sprintf(recordBody, "rm-alpha", "alpha/one", 11),
		"github.com/beta/two#22":  fmt.Sprintf(recordBody, "rm-beta", "beta/two", 22),
		// A null record and an undecodable body must both stay local errors.
		"github.com/beta/nil#33":  `{"record":null}`,
		"github.com/gamma/bad#44": `{"record":"not a record"}`,
	})

	s := watch.New()
	s.Store = &watch.Store{Root: prstate.Root(store.DataDir())}
	var mu sync.Mutex
	var calls []string
	stub := func(_ context.Context, roomRef, key string, _ bool, _ string) error {
		mu.Lock()
		calls = append(calls, roomRef+" "+key)
		mu.Unlock()
		return nil
	}
	bound, err := websvc.Bind([]websvc.Binding{{Name: "SetPRDraft", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: stub}})
	if err != nil {
		t.Fatal(err)
	}
	enginetest.Serve(t, store.DataDir(), bound)

	run := func(key string) error {
		cmd := Command(Options{Service: func() *watch.Service { return s }})
		cmd.SetArgs([]string{"hold", prURL(key), "test reason"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}

	// Each hold reaches the engine with its own room and key.
	for _, key := range []string{"github.com/alpha/one#11", "github.com/beta/two#22"} {
		if err := run(key); err != nil {
			t.Fatal(err)
		}
	}
	// Missing and null-record keys fail before any engine call.
	for _, key := range []string{"github.com/beta/nil#33", "github.com/beta/gone#99"} {
		err := run(key)
		if err == nil || !strings.Contains(err.Error(), "PR is not tracked") {
			t.Fatalf("hold of %s: got %v, want PR is not tracked", key, err)
		}
	}
	mu.Lock()
	routed := append([]string{}, calls...)
	mu.Unlock()
	want := []string{"rm-alpha github.com/alpha/one#11", "rm-beta github.com/beta/two#22"}
	if fmt.Sprint(routed) != fmt.Sprint(want) {
		t.Fatalf("engine calls %v, want %v", routed, want)
	}
}
