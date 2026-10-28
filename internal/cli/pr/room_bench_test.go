package pr

// This file times the hold command end to end against a real copied PR
// database, so a state-read change ships with measured before and after
// numbers. It is skipped unless PR_BENCH_DB names a copied dyna.db and
// PR_BENCH_KEY names a tracked PR key inside it; CI never sets them. Each
// fresh go-test process pays the same cold read a real CLI process pays.

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/hub"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
	"dyna-agent/internal/websvc/devicestore"
)

// serveBenchEngine answers engine calls on the CLI socket, like enginetest
// Serve, but without running store migrations: a copied database may hold
// historical rows that only the live engine already migrated past.
func serveBenchEngine(t *testing.T, data string, calls map[string]websvc.Call) {
	t.Helper()
	store, err := devicestore.OpenStore(filepath.Join(data, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := websvc.New(websvc.Options{Store: store, Routes: websvc.LoopbackRoutes("7457", "7458"),
		Shell: fstest.MapFS{"index.html": {Data: []byte("<html><head></head></html>")}}, Calls: calls,
		Hub: hub.New(1), Files: http.NotFoundHandler(), Cards: http.NotFoundHandler(), Version: "bench",
		Callers: func(net.Conn) (websvc.Caller, error) { return websvc.Caller{Kind: websvc.Owner}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	// A Unix socket path is limited to about 100 bytes, so the runtime
	// folder goes straight under /tmp, as enginetest does.
	base := ""
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		base = "/tmp"
	}
	runtime, err := os.MkdirTemp(base, "dyna-bench-")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	path, err := engineclient.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: srv.UnixHandler(), ConnContext: srv.UnixConnContext}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve: %v", err)
		}
	})
}

func TestHoldCommandAgainstCopiedStore(t *testing.T) {
	src := os.Getenv("PR_BENCH_DB")
	if src == "" {
		t.Skip("set PR_BENCH_DB to a copied dyna.db and PR_BENCH_KEY to a tracked PR key in it")
	}
	key := os.Getenv("PR_BENCH_KEY")
	data := storetest.DataDir(t)
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	// A reflink copy keeps each run isolated from the source database.
	if out, err := exec.Command("cp", "--reflink=auto", src, filepath.Join(data, "dyna.db")).CombinedOutput(); err != nil {
		t.Fatalf("copy benchmark database: %v: %s", err, out)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	var want string
	if err = db.QueryRow("SELECT json_extract(body,'$.record.roomId') FROM pr_rows WHERE key=?", key).Scan(&want); err != nil {
		t.Fatalf("read benchmark row: %v", err)
	}

	s := watch.New()
	s.Store = &watch.Store{Root: prstate.Root(data)}
	routed := make(chan string, 4)
	stub := func(_ context.Context, roomRef, _ string, _ bool, _ string) error {
		routed <- roomRef
		return nil
	}
	calls, err := websvc.Bind([]websvc.Binding{{Name: "SetPRDraft", Access: websvc.Write, Scope: websvc.ScopeOperate, Method: stub}})
	if err != nil {
		t.Fatal(err)
	}
	serveBenchEngine(t, data, calls)

	run := func() time.Duration {
		cmd := Command(Options{Service: func() *watch.Service { return s }})
		cmd.SetArgs([]string{"hold", prURL(key), "benchmark"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		start := time.Now()
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	cold := run()
	if got := <-routed; got != want {
		t.Fatalf("hold routed to %s, want %s", got, want)
	}
	t.Logf("hold cold total: %v", cold)
	for i := 0; i < 3; i++ {
		t.Logf("hold warm total: %v", run())
	}
}
