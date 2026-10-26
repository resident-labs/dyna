// Package scratch owns private, disposable files for Dyna workers.
package scratch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/protectedpath"
	"dyna-agent/internal/store"
)

const Env = "DYNA_SCRATCH"

// RootEnv overrides the disk base that holds a store's scratch namespace.
// Tests and tools use it; normal runs leave it unset.
const RootEnv = "DYNA_SCRATCH_ROOT"

const ClosedRetention = 24 * time.Hour
const UnknownRetention = 3 * 24 * time.Hour

// Root is the disk folder that holds one store's scratch: a short namespace
// derived from the canonical data folder under the owner's cache base. It
// never follows TMPDIR or a harness home: /tmp is a RAM disk with a per-user
// quota, and a worker's HOME/XDG_CACHE_HOME is a private harness home. The
// owner home comes from the account database, so the engine, a runner and a
// nested dyna all resolve the same base. The store namespace keeps a sweep in
// one data folder away from another store's open sessions. The path stays
// short so sockets under a session's scratch remain below the 108-byte unix
// socket limit.
func Root() string {
	base := strings.TrimSpace(os.Getenv(RootEnv))
	if base == "" {
		owner, err := user.Current()
		if err != nil || owner.HomeDir == "" {
			// Never fall back to $HOME: a harness home would split scratch per
			// account instead of using the one owner folder.
			return ""
		}
		base = filepath.Join(owner.HomeDir, ".cache", "dyna", "scratch")
	}
	base, err := filepath.Abs(base)
	if err != nil {
		return ""
	}
	return filepath.Join(base, storeNamespace(store.DataDir()))
}

// storeNamespace derives the short, stable namespace of one data folder. A
// few stores share the disk base, so eight hex characters are ample.
func storeNamespace(dataDir string) string {
	canonical, err := filepath.Abs(dataDir)
	if err != nil {
		canonical = dataDir
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:4])
}

// validateRoot rejects shared or foreign-owned roots. Workers receive a normal
// absolute path, so the parent must not let another user replace their folders.
func validateRoot(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("scratch root must be a real directory owned by the current user with mode 0700: %s", path)
	}
	return nil
}

func validOwner(owner string) bool {
	if owner == "" || filepath.IsAbs(owner) {
		return false
	}
	for _, part := range strings.Split(owner, string(os.PathSeparator)) {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return false
		}
	}
	return true
}

// Ensure prepares a private session directory or run/worker directory.
func Ensure(owner string) (string, error) {
	if !validOwner(owner) {
		return "", fmt.Errorf("invalid scratch owner %q", owner)
	}
	root := Root()
	if root == "" {
		return "", errors.New("cannot resolve scratch root")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if err := validateRoot(root, info); err != nil {
		return "", err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer r.Close()
	name := ""
	for _, part := range strings.Split(owner, string(os.PathSeparator)) {
		name = filepath.Join(name, part)
		if err = r.Mkdir(name, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		info, err := r.Lstat(name)
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", fmt.Errorf("scratch directory must be a real directory with mode 0700: %s", name)
		}
	}
	return filepath.Join(root, owner), nil
}

type Owner struct {
	Known, Open bool
	ClosedAt    time.Time
}

type Options struct {
	Logf func(string, ...any)
	Now  time.Time
	// Lookup receives a top-level session or run ID. All workers of a run
	// share its retention date. A nil Lookup treats every owner as unknown.
	Lookup   func(owner string) Owner
	Pressure bool
	// Closing marks the pass that a closing session runs. Its own scratch goes
	// now, because the closed retention exists to let a parent read a finished
	// session's files, and a closing session's caller has already read them.
	Closing bool
	// Only limits the sweep to a relative owner path and its descendants.
	Only string
}

type Kept struct{ Path, Reason string }
type Result struct {
	Removed    []string
	Kept       []Kept
	BytesFreed int64
}

// Sweep expires scratch files. Confined operations and WalkDir do not follow
// links outside Root. An open owner is always kept, even under disk pressure.
func Sweep(ctx context.Context, o Options) (Result, error) {
	result := Result{Removed: []string{}, Kept: []Kept{}}
	if o.Only != "" && !validOwner(o.Only) {
		return result, fmt.Errorf("invalid scratch owner %q", o.Only)
	}
	root := Root()
	if root == "" {
		return result, errors.New("cannot resolve scratch root")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if err := validateRoot(root, info); err != nil {
		return result, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return result, err
	}
	defer r.Close()
	names := []string{o.Only}
	if o.Only == "" {
		entries, err := fs.ReadDir(r.FS(), ".")
		if err != nil {
			return result, err
		}
		names = nil
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
	}
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := r.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return result, err
		}
		owner := Owner{}
		if o.Lookup != nil {
			owner = o.Lookup(strings.Split(name, string(os.PathSeparator))[0])
		}
		path := filepath.Join(root, name)
		keep := func(reason string) { result.Kept = append(result.Kept, Kept{Path: path, Reason: reason}) }
		protected := func(err error) {
			protectedpath.LogOnce(path, o.Logf, "protected scratch path %s: %v; skipping cleanup", path, err)
			keep("protected path cannot be removed")
		}
		if owner.Open {
			keep("owner is open")
			continue
		}
		if owner.Known {
			if owner.ClosedAt.IsZero() {
				keep("owner has no close time")
				continue
			}
			if !o.Closing && !o.Pressure && now.Before(owner.ClosedAt.Add(ClosedRetention)) {
				keep("closed retention period")
				continue
			}
		}
		newest, bytes, err := inspect(ctx, r, name)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			protected(err)
			continue
		}
		if !owner.Known {
			retention := UnknownRetention
			if o.Pressure {
				retention = 24 * time.Hour
			}
			if now.Before(newest.Add(retention)) {
				keep("unknown owner has recent changes")
				continue
			}
		}
		if err := r.RemoveAll(name); err != nil {
			protected(err)
			continue
		}
		result.Removed = append(result.Removed, path)
		result.BytesFreed += bytes
	}
	return result, nil
}

// inspect counts regular file bytes and checks every entry's modification time.
// Lstat also prevents following an owner's top-level symbolic link.
func inspect(ctx context.Context, r *os.Root, name string) (time.Time, int64, error) {
	newest := time.Time{}
	var bytes int64
	visit := func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		info, err := r.Lstat(path)
		if err != nil {
			return err
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		if info.Mode().IsRegular() {
			bytes += info.Size()
		}
		return nil
	}
	info, err := r.Lstat(name)
	if err != nil {
		return newest, bytes, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		err = visit(name, nil, nil)
	} else {
		err = fs.WalkDir(r.FS(), name, visit)
	}
	return newest, bytes, err
}
