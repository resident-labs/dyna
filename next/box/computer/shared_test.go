package computer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeProfileStore struct {
	mu          sync.Mutex
	checkoutErr error
	lease       Lease
	seedFile    string
	checkinErr  error
	checkins    []fakeCheckin
}

type fakeCheckin struct {
	lease   Lease
	discard bool
	cookie  string
}

func (f *fakeProfileStore) Checkout(_ context.Context, dir string) (Lease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkoutErr != nil {
		return Lease{}, f.checkoutErr
	}
	if f.lease.Profile == "" {
		return Lease{}, nil
	}
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0700); err != nil {
		return Lease{}, err
	}
	if f.seedFile != "" {
		if err := os.WriteFile(filepath.Join(dir, "Default", "Cookies"), []byte(f.seedFile), 0600); err != nil {
			return Lease{}, err
		}
	}
	return f.lease, nil
}

func (f *fakeProfileStore) Checkin(_ context.Context, lease Lease, dir string, discard bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var cookie string
	if data, err := os.ReadFile(filepath.Join(dir, "Default", "Cookies")); err == nil {
		cookie = string(data)
	}
	f.checkins = append(f.checkins, fakeCheckin{lease: lease, discard: discard, cookie: cookie})
	return f.checkinErr
}

func (f *fakeProfileStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.checkins)
}

func (f *fakeProfileStore) last() fakeCheckin {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkins[len(f.checkins)-1]
}

func fakeSharedDesktop(t *testing.T, chromiumScript string, store ProfileStore) (*Process, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "computer"), 0700); err != nil {
		t.Fatal(err)
	}
	scripts := map[string]string{
		"Xvnc":     "#!/bin/sh\nwhile [ \"$1\" != -rfbunixpath ]; do shift; done\ntouch \"$2\"\nexec sleep 600\n",
		"jwm":      "#!/bin/sh\nexec sleep 600\n",
		"chromium": chromiumScript,
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(root, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(filepath.Join(tmp, ".X11-unix"), 0700); err != nil {
		t.Fatal(err)
	}
	p := &Process{
		root:          root,
		socket:        filepath.Join(root, "run", "vnc.sock"),
		tmp:           tmp,
		profile:       filepath.Join(root, "profile"),
		sharedProfile: filepath.Join(root, "computer-shared"),
		devtoolsURL:   "http://127.0.0.1:1", // refuses fast so tests exercise SIGTERM/deadline deterministically
		store:         store,
		idleTimeout:   defaultIdleTimeout,
		closeTimeout:  2 * time.Second,
	}
	if err := os.Mkdir(p.profile, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, root
}

func TestSharedCheckoutRefusalDoesNotStartDesktop(t *testing.T) {
	const refusal = "browser profile work is in use by alice in room ops; it frees when that session's computer stops (computer stop) or goes idle"
	store := &fakeProfileStore{checkoutErr: errors.New(refusal)}
	p, root := fakeSharedDesktop(t, "#!/bin/sh\necho started > "+filepath.Join(t.TempDir(), "x")+"\nexec sleep 600\n", store)
	_, err := p.Start(context.Background())
	if err == nil || err.Error() != refusal {
		t.Fatalf("Start err = %v, want %q", err, refusal)
	}
	p.mu.Lock()
	running := p.display != nil || p.wm != nil || p.browser != nil
	p.mu.Unlock()
	if running {
		t.Fatal("desktop started despite checkout refusal")
	}
	if _, err := os.Stat(filepath.Join(root, "run", "vnc.sock")); !os.IsNotExist(err) {
		t.Fatalf("Xvnc ran despite checkout refusal: %v", err)
	}
}

func TestSharedCheckoutCleanStopAndDeleteLocalCopy(t *testing.T) {
	store := &fakeProfileStore{
		lease:    Lease{Profile: "work", Generation: 2},
		seedFile: "initial=1",
	}
	outArgs := filepath.Join(t.TempDir(), "chromium.args")
	// Fake Chromium writes an updated cookie when it receives SIGTERM and exits cleanly.
	script := `#!/bin/sh
echo "$*" > ` + outArgs + `
for arg in "$@"; do
  case "$arg" in
    --user-data-dir=*) dir="${arg#--user-data-dir=}" ;;
  esac
done
trap 'printf "updated=2" > "$dir/Default/Cookies"; exit 0' TERM
while true; do sleep 1; done
`
	p, _ := fakeSharedDesktop(t, script, store)
	if err := os.WriteFile(filepath.Join(p.profile, "local-marker"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool { data, _ := os.ReadFile(outArgs); return len(data) > 0 })
	args, _ := os.ReadFile(outArgs)
	if !strings.Contains(string(args), "--user-data-dir="+p.sharedProfile) {
		t.Fatalf("Chromium did not use sharedProfile: %s", args)
	}

	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("checkins = %d, want 1", store.count())
	}
	got := store.last()
	if got.discard || got.cookie != "updated=2" || got.lease != store.lease {
		t.Fatalf("checkin = %+v", got)
	}
	if _, err := os.Stat(p.sharedProfile); !os.IsNotExist(err) {
		t.Fatalf("sharedProfile not deleted after checkin: %v", err)
	}
	// Per-box profile must remain untouched.
	if marker, err := os.ReadFile(filepath.Join(p.profile, "local-marker")); err != nil || string(marker) != "keep" {
		t.Fatalf("per-box profile changed: %q %v", marker, err)
	}
}

func TestSharedKilledChromiumSkipsCheckin(t *testing.T) {
	store := &fakeProfileStore{
		lease:    Lease{Profile: "work", Generation: 3},
		seedFile: "initial=1",
	}
	ready := filepath.Join(t.TempDir(), "ready")
	// Fake Chromium ignores SIGTERM so Stop must SIGKILL it.
	script := "#!/bin/sh\ntrap '' TERM\ntouch " + ready + "\nwhile true; do sleep 1; done\n"
	p, _ := fakeSharedDesktop(t, script, store)
	p.closeTimeout = 250 * time.Millisecond

	if _, err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool { _, err := os.Stat(ready); return err == nil })
	if err := p.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("checkins = %d, want 1", store.count())
	}
	if !store.last().discard {
		t.Fatalf("killed Chromium must skip checkin (discard=true), got %+v", store.last())
	}
	if _, err := os.Stat(p.sharedProfile); !os.IsNotExist(err) {
		t.Fatalf("sharedProfile not deleted: %v", err)
	}
}

func TestSharedKilledChromiumRetryStillDiscards(t *testing.T) {
	store := &fakeProfileStore{
		lease:      Lease{Profile: "work", Generation: 3},
		seedFile:   "initial=1",
		checkinErr: errors.New("temporary disconnect"),
	}
	ready := filepath.Join(t.TempDir(), "ready")
	script := "#!/bin/sh\ntrap '' TERM\ntouch " + ready + "\nwhile true; do sleep 1; done\n"
	p, _ := fakeSharedDesktop(t, script, store)
	p.closeTimeout = 250 * time.Millisecond

	if _, err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFor(t, func() bool { _, err := os.Stat(ready); return err == nil })
	if err := p.Stop(); err == nil {
		t.Fatal("expected first Stop to return transient checkin error")
	}
	store.mu.Lock()
	store.checkinErr = nil
	store.mu.Unlock()
	if err := p.Stop(); err != nil {
		t.Fatalf("retry Stop: %v", err)
	}
	if store.count() != 2 || !store.last().discard {
		t.Fatalf("retry after killed Chromium must still discard, got count=%d last=%+v", store.count(), store.last())
	}
}

func TestSharedIdleReleaseAndViewerPause(t *testing.T) {
	store := &fakeProfileStore{
		lease:    Lease{Profile: "work", Generation: 4},
		seedFile: "cookie=ok",
	}
	script := "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 1; done\n"
	p, _ := fakeSharedDesktop(t, script, store)
	p.idleTimeout = 150 * time.Millisecond

	srv := httptest.NewServer(p.Handler(context.Background()))
	defer srv.Close()

	// Start desktop with a VNC viewer connected; idle timer must NOT fire while viewer stays open.
	if _, err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pr, pw := io.Pipe()
	viewerDone := make(chan struct{})
	go func() {
		defer close(viewerDone)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/computer/viewer", pr)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.viewers == 1 })

	// Sleep past idleTimeout while viewer is connected: lease must remain held.
	time.Sleep(300 * time.Millisecond)
	if store.count() != 0 {
		t.Fatalf("idle release fired while VNC viewer was connected")
	}

	// Disconnect viewer; idle timer starts and releases the profile.
	_ = pw.Close()
	<-viewerDone

	waitFor(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return store.count() == 1 && p.display == nil && p.browser == nil && p.lease.Profile == ""
	})
	if store.last().discard {
		t.Fatalf("idle release should be a clean checkin, got %+v", store.last())
	}
}
