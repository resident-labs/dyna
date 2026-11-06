package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"text/template"
	"time"
)

// These are native Bash/filesystem/flock tests, with fixture Podman output and a local SSH stand-in.
// They do not exercise a live Podman daemon or SSH server.
type binaryGCRunner struct {
	path, fixture string
	beforeDelete  func()
}

func (r *binaryGCRunner) Run(ctx context.Context, in []byte, args ...string) ([]byte, error) {
	command := args[len(args)-1]
	if r.beforeDelete != nil && strings.Contains(command, "'dyna-prune-binaries'") {
		r.beforeDelete()
	}
	args = append([]string(nil), args...)
	args[len(args)-1] = "export PATH=" + quote(r.path+":"+os.Getenv("PATH")) + "; export GC_FIXTURE=" + quote(r.fixture) + "; " + command
	return (nativeShellRunner{}).Run(ctx, in, args...)
}

type binaryGCRig struct {
	t                                            *testing.T
	h                                            *Host
	r                                            *binaryGCRunner
	bin, fixture                                 string
	wanted, old, mounted, middle, recent, newest string
}

type binaryGCContainer struct {
	ID     string `json:"Id"`
	Mounts []binaryGCMount
	State  struct{ Status string }
}
type binaryGCMount struct{ Type, Source string }

func newBinaryGCRig(t *testing.T) *binaryGCRig {
	t.Helper()
	rig := &binaryGCRig{t: t, fixture: t.TempDir()}
	rig.bin = filepath.Join(t.TempDir(), "binary store\nwith newline\n")
	if err := os.Mkdir(rig.bin, 0700); err != nil {
		t.Fatal(err)
	}
	values := []*string{&rig.wanted, &rig.old, &rig.mounted, &rig.middle, &rig.recent, &rig.newest}
	for i, field := range values {
		*field = strings.Repeat(fmt.Sprintf("%x", i+10), 64)
		dir := filepath.Join(rig.bin, *field)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		rig.write(filepath.Join(dir, "dyna"), []byte("fixture binary"))
		rig.mtime(*field, time.Unix(1700000000+int64(i), int64(i)))
	}
	path := t.TempDir()
	rig.write(filepath.Join(path, "podman"), []byte(`#!/bin/bash
set -eu
if [ "$1" = ps ]; then
 [[ " $* " == *' --all '* && " $* " == *' --no-trunc '* ]] || exit 90
 if [ "${@: -1}" = json ]; then
  [ ! -f "$GC_FIXTURE/ps-error" ] || exit 91
  cat "$GC_FIXTURE/ps.json"
 else
  [ "${@: -1}" = '{{.ID}}' ] || exit 92
  touch "$GC_FIXTURE/locked-read"
  [ ! -f "$GC_FIXTURE/locked-ps-error" ] || exit 93
  cat "$GC_FIXTURE/ids"
 fi
elif [ "$1" = container ] && [ "$2" = inspect ]; then
 if [ "$3" = --format ]; then
  printf '%s' "$4" > "$GC_FIXTURE/template"
  id=${@: -1}
  cat "$GC_FIXTURE/$id.mounts"
  [ ! -f "$GC_FIXTURE/$id.error" ] || exit 94
 else
  [ "$3" = -- ] || exit 95
  cat "$GC_FIXTURE/inspect.json"
  [ ! -f "$GC_FIXTURE/inspect-error" ] || exit 96
 fi
else
 exit 97
fi
`))
	if err := os.Chmod(filepath.Join(path, "podman"), 0700); err != nil {
		t.Fatal(err)
	}
	rig.r = &binaryGCRunner{path: path, fixture: rig.fixture}
	var err error
	rig.h, err = New("user@host", t.TempDir(), "host", rig.r)
	if err != nil {
		t.Fatal(err)
	}
	rig.containers()
	return rig
}

func (r *binaryGCRig) write(path string, data []byte) {
	r.t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		r.t.Fatal(err)
	}
}
func (r *binaryGCRig) file(name, contents string) {
	r.t.Helper()
	r.write(filepath.Join(r.fixture, name), []byte(contents))
}
func (r *binaryGCRig) mtime(sum string, when time.Time) {
	r.t.Helper()
	if err := os.Chtimes(filepath.Join(r.bin, sum), when, when); err != nil {
		r.t.Fatal(err)
	}
}
func (r *binaryGCRig) containers(containers ...binaryGCContainer) {
	r.t.Helper()
	ps := make([]map[string]string, 0, len(containers))
	if containers == nil {
		containers = []binaryGCContainer{}
	}
	var ids strings.Builder
	for _, c := range containers {
		ps = append(ps, map[string]string{"Id": c.ID})
		fmt.Fprintln(&ids, c.ID)
		var out strings.Builder
		fmt.Fprintf(&out, "%s\x00MOUNTS\x00", c.ID)
		for _, m := range c.Mounts {
			fmt.Fprintf(&out, "M\x00%s\x00%s\x00", m.Type, m.Source)
		}
		out.WriteString("END\x00\n")
		r.file(c.ID+".mounts", out.String())
	}
	psJSON, err := json.Marshal(ps)
	if err != nil {
		r.t.Fatal(err)
	}
	inspectJSON, err := json.Marshal(containers)
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(filepath.Join(r.fixture, "ps.json"), psJSON)
	r.write(filepath.Join(r.fixture, "inspect.json"), inspectJSON)
	r.file("ids", ids.String())
}
func (r *binaryGCRig) mountedContainer(source string) binaryGCContainer {
	c := binaryGCContainer{ID: strings.Repeat("1", 64), Mounts: []binaryGCMount{{"bind", source}}}
	c.State.Status = "exited"
	return c
}
func (r *binaryGCRig) prune() error { return r.h.PruneBinaries(context.Background(), r.bin, r.wanted) }
func (r *binaryGCRig) exists(want bool, sums ...string) {
	r.t.Helper()
	for _, sum := range sums {
		_, err := os.Lstat(filepath.Join(r.bin, sum))
		if (err == nil) != want || (err != nil && !os.IsNotExist(err)) {
			r.t.Fatalf("object %s: wanted present=%v, got %v", sum, want, err)
		}
	}
}
func (r *binaryGCRig) allPresent() {
	r.t.Helper()
	r.exists(true, r.wanted, r.old, r.mounted, r.middle, r.recent, r.newest)
}

func TestPruneBinariesRetainsWantedRollbackAndStoppedMount(t *testing.T) {
	r := newBinaryGCRig(t)
	c := r.mountedContainer(filepath.Join(r.bin, r.mounted, "dyna"))
	r.containers(c)
	if err := r.prune(); err != nil {
		t.Fatal(err)
	}
	r.exists(true, r.wanted, r.mounted, r.recent, r.newest)
	r.exists(false, r.old, r.middle)
	// Check the real Go template supplied to Podman, rather than trusting fixture bytes alone.
	format, err := os.ReadFile(filepath.Join(r.fixture, "template"))
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("mounts").Parse(string(format))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, c); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(r.fixture, c.ID+".mounts"))
	if err != nil {
		t.Fatal(err)
	}
	if out.String()+"\n" != string(want) {
		t.Fatalf("template output %q, want %q", out.String(), want)
	}
}

func TestPruneBinariesKeepsTwoNewestOtherObjectsWithDeterministicTies(t *testing.T) {
	r := newBinaryGCRig(t)
	for _, sum := range []string{r.old, r.mounted, r.middle, r.recent, r.newest} {
		r.mtime(sum, time.Unix(1700000001, 123))
	}
	if err := r.prune(); err != nil {
		t.Fatal(err)
	}
	r.exists(true, r.wanted, r.old, r.mounted) // Ascending SHA breaks equal-mtime ties.
	r.exists(false, r.middle, r.recent, r.newest)
}

func TestPruneBinariesConservativelyKeepsBroadAndLegacyMounts(t *testing.T) {
	for _, name := range []string{"store", "ancestor", "root", "current", "legacy", "alias-current"} {
		t.Run(name, func(t *testing.T) {
			r := newBinaryGCRig(t)
			if err := os.Symlink(r.old, filepath.Join(r.bin, "current")); err != nil {
				t.Fatal(err)
			}
			var source string
			switch name {
			case "store":
				source = r.bin
			case "ancestor":
				source = filepath.Dir(r.bin)
			case "root":
				source = "/"
			case "current":
				source = filepath.Join(r.bin, "current", "dyna")
			case "legacy":
				source = filepath.Join(r.bin, "v1", "dyna")
			case "alias-current":
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(r.bin, alias); err != nil {
					t.Fatal(err)
				}
				source = filepath.Join(alias, "current", "dyna")
			}
			r.containers(r.mountedContainer(source))
			if err := r.prune(); err != nil {
				t.Fatal(err)
			}
			r.allPresent()
		})
	}
}

func TestPruneBinariesInitialInventoryFailuresDeleteNothing(t *testing.T) {
	for _, name := range []string{"ps-error", "ps-null", "ps-malformed", "ps-missing-id", "inspect-error", "inspect-malformed", "inspect-partial", "inspect-extra", "inspect-wrong-id", "inspect-duplicate-id", "mounts-missing", "mounts-null", "source-missing", "source-relative", "type-missing"} {
		t.Run(name, func(t *testing.T) {
			r := newBinaryGCRig(t)
			c := r.mountedContainer("/unrelated/source")
			r.containers(c)
			switch name {
			case "ps-error", "inspect-error":
				r.file(name, "")
			case "ps-null":
				r.file("ps.json", "null")
			case "ps-malformed":
				r.file("ps.json", "[")
			case "ps-missing-id":
				r.file("ps.json", "[{}]")
			case "inspect-malformed":
				r.file("inspect.json", "[")
			case "inspect-partial":
				r.file("inspect.json", "[]")
			case "inspect-extra":
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q,"Mounts":[]},{}]`, c.ID))
			case "inspect-wrong-id":
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q,"Mounts":[]}]`, strings.Repeat("2", 64)))
			case "inspect-duplicate-id":
				second := c
				second.ID = strings.Repeat("2", 64)
				r.containers(c, second)
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q,"Mounts":[]},{"Id":%q,"Mounts":[]}]`, c.ID, c.ID))
			case "mounts-missing":
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q}]`, c.ID))
			case "mounts-null":
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q,"Mounts":null}]`, c.ID))
			case "source-missing":
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q,"Mounts":[{"Type":"bind"}]}]`, c.ID))
			case "source-relative":
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q,"Mounts":[{"Type":"bind","Source":"relative"}]}]`, c.ID))
			case "type-missing":
				r.file("inspect.json", fmt.Sprintf(`[{"Id":%q,"Mounts":[{"Source":"/path"}]}]`, c.ID))
			}
			if err := r.prune(); err == nil {
				t.Fatal("invalid inventory succeeded")
			}
			r.allPresent()
		})
	}
}

func TestPruneBinariesLockedInventoryFailuresDeleteNothing(t *testing.T) {
	for _, name := range []string{"ps-error", "invalid-id", "duplicate-id", "inspect-error", "missing-inspect", "partial-inspect", "missing-mounts", "wrong-id", "extra-inspect", "invalid-source", "invalid-type"} {
		t.Run(name, func(t *testing.T) {
			r := newBinaryGCRig(t)
			first := r.mountedContainer("/unrelated/source")
			second := first
			second.ID = strings.Repeat("2", 64)
			r.containers(first, second)
			switch name {
			case "ps-error":
				r.file("locked-ps-error", "")
			case "invalid-id":
				r.file("ids", "not-an-id\n")
			case "duplicate-id":
				r.file("ids", first.ID+"\n"+first.ID+"\n")
			case "inspect-error":
				r.file(second.ID+".error", "")
			case "missing-inspect":
				r.file(second.ID+".mounts", "")
			case "partial-inspect":
				r.file(second.ID+".mounts", second.ID+"\x00MOUNTS\x00M\x00bind\x00/unrelated\x00")
			case "missing-mounts":
				r.file(second.ID+".mounts", second.ID+"\x00INVALID\x00")
			case "wrong-id":
				r.file(second.ID+".mounts", first.ID+"\x00MOUNTS\x00END\x00\n")
			case "extra-inspect":
				r.file(second.ID+".mounts", second.ID+"\x00MOUNTS\x00END\x00\nextra\x00")
			case "invalid-source":
				r.file(second.ID+".mounts", second.ID+"\x00MOUNTS\x00M\x00bind\x00relative\x00END\x00\n")
			case "invalid-type":
				r.file(second.ID+".mounts", second.ID+"\x00MOUNTS\x00M\x00unknown\x00/absolute\x00END\x00\n")
			}
			if err := r.prune(); err == nil {
				t.Fatal("invalid locked inventory succeeded")
			}
			r.allPresent()
		})
	}
}

func TestPruneBinariesReReadsMountsWhileHoldingCreationLock(t *testing.T) {
	r := newBinaryGCRig(t)
	lock, err := os.OpenFile(filepath.Join(r.bin, ".activation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	entered := make(chan struct{})
	r.r.beforeDelete = func() { close(entered) }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.h.PruneBinaries(ctx, r.bin, r.wanted) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("did not reach delete phase")
	}
	time.Sleep(80 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(r.fixture, "locked-read")); !os.IsNotExist(err) {
		t.Fatalf("inventory did not wait for creation lock: %v", err)
	}
	r.allPresent()
	// Simulate podman create publishing a stopped container pin before releasing the same lock.
	r.containers(r.mountedContainer(filepath.Join(r.bin, r.old)))
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	r.exists(true, r.wanted, r.old, r.recent, r.newest)
	r.exists(false, r.mounted, r.middle)
}

func TestPruneBinariesChangedLegacyReferenceKeepsEverything(t *testing.T) {
	r := newBinaryGCRig(t)
	r.r.beforeDelete = func() { r.containers(r.mountedContainer(filepath.Join(r.bin, "current", "dyna"))) }
	if err := r.prune(); err != nil {
		t.Fatal(err)
	}
	r.allPresent()
}

func TestPruneBinariesRechecksPublicationMetadata(t *testing.T) {
	r := newBinaryGCRig(t)
	r.r.beforeDelete = func() { r.mtime(r.old, time.Now()) }
	if err := r.prune(); err != nil {
		t.Fatal(err)
	}
	r.exists(true, r.wanted, r.old, r.recent, r.newest)
	r.exists(false, r.mounted, r.middle)
}

func TestPruneBinariesLeavesNonObjectsSymlinksAndActiveStages(t *testing.T) {
	r := newBinaryGCRig(t)
	if err := os.RemoveAll(filepath.Join(r.bin, r.old)); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	r.write(filepath.Join(outside, "dyna"), []byte("do not remove"))
	if err := os.Symlink(outside, filepath.Join(r.bin, r.old)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(r.bin, "legacy-version"), 0700); err != nil {
		t.Fatal(err)
	}
	r.write(filepath.Join(r.bin, "WANT"), []byte("legacy"))
	if err := os.Symlink(r.wanted, filepath.Join(r.bin, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(r.bin, r.mounted, ".dyna.active-stage"), 0700); err != nil {
		t.Fatal(err)
	}
	r.mtime(r.mounted, time.Unix(1700000002, 2))
	if err := r.prune(); err != nil {
		t.Fatal(err)
	}
	r.exists(true, r.wanted, r.old, r.mounted, r.recent, r.newest, "legacy-version", "WANT", "current")
	r.exists(false, r.middle)
	if _, err := os.Stat(filepath.Join(outside, "dyna")); err != nil {
		t.Fatal(err)
	}
}

func TestPruneBinariesCancellationCannotLeaveALateDeleter(t *testing.T) {
	r := newBinaryGCRig(t)
	r.write(filepath.Join(r.r.path, "flock"), []byte("#!/bin/bash\ntouch \"$GC_FIXTURE/lock-wait\"\nexec /usr/bin/flock \"$@\"\n"))
	if err := os.Chmod(filepath.Join(r.r.path, "flock"), 0700); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(r.bin, ".activation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	entered := make(chan struct{})
	r.r.beforeDelete = func() { close(entered) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.h.PruneBinaries(ctx, r.bin, r.wanted) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("did not reach delete phase")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(r.fixture, "lock-wait")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("remote prune did not try to lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled prune succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("remote prune was not joined")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	r.allPresent()
	if _, err := os.Stat(filepath.Join(r.fixture, "locked-read")); !os.IsNotExist(err) {
		t.Fatalf("cancelled command escaped lock: %v", err)
	}
}

func TestPruneBinariesRejectsInvalidWantedBeforeRemoteCalls(t *testing.T) {
	r := newBinaryGCRig(t)
	for _, sum := range []string{"", "../current", strings.Repeat("F", 64)} {
		if err := r.h.PruneBinaries(context.Background(), r.bin, sum); err == nil {
			t.Fatalf("accepted %q", sum)
		}
	}
	r.allPresent()
}
