package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"dyna-agent/internal/account/native"
	"dyna-agent/internal/account/store"
	harnesshome "dyna-agent/internal/harness/home"
)

var (
	errOwnerLoginRetry = errors.New("Copying your personal login did not finish. Try again.")
	// errLoginRead marks a failed native login read, such as a failed account
	// command. It is transient, so the copy can run again.
	errLoginRead       = errors.New("A native login could not be read:")
	errOwnerLoginBusy  = errors.New("The owner login store is busy.")
	errNoPersonalLogin = errors.New("Dyna found no personal login to copy. Sign in instead.")
	errCopyFailed      = errors.New("Dyna could not copy your personal login. Sign in instead.")
	errPrivateHomeUsed = errors.New("Dyna's private home for this harness already holds a login. Sign in instead.")
)

// copyPersonalLogin copies the owner's native login into Dyna's private home,
// because the user chose it. It never replaces files in that home. A pending
// file marks a copy that wrote private files but did not finish, so the next
// attempt indexes them instead of refusing.
func (r *Runtime) copyPersonalLogin(ctx context.Context, harness string) (resultErr error) {
	h, err := canonicalAccountHarness(harness)
	if err != nil {
		return err
	}
	a := r.Accounts
	if a == nil {
		return errors.New("Private account storage is unavailable.")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	dir, err := a.harnessDirectory(h)
	if err != nil {
		return err
	}
	lock, err := acquireAccountLock(ctx, filepath.Join(dir, ".owner-login-import.lock"), syscall.LOCK_EX)
	if err != nil {
		if ctx.Err() != nil {
			return errors.Join(errOwnerLoginRetry, ctx.Err())
		}
		return errors.New("The owner login import lock is unavailable.")
	}
	defer releaseAccountLock(lock)
	pending := filepath.Join(dir, ".owner-login-import.pending")
	// A crash, deadline, busy owner store or failed native command keeps the
	// pending file, so the next attempt can finish. A completed copy removes
	// it even if its context expired during file writes or indexing.
	retry := false
	defer func() {
		if resultErr != nil && (retry || errors.Is(resultErr, errLoginRead) || ctx.Err() != nil) {
			resultErr = errors.Join(errOwnerLoginRetry, resultErr, ctx.Err())
			return
		}
		if e := os.Remove(pending); resultErr == nil && e != nil && !os.IsNotExist(e) {
			resultErr = errAccountPath
		}
	}()
	scope, err := a.HarnessHome(h)
	if err != nil {
		return err
	}
	if err = checkPrivateAccountFile(pending, false); err != nil {
		return err
	}
	resume := false
	if _, err = os.Lstat(pending); err == nil {
		resume = true
	} else if !os.IsNotExist(err) {
		return errAccountPath
	}
	// A positive emptiness check protects native state with no saved index too.
	for _, path := range []string{scope, harnesshome.Home(scope)} {
		if safeAccountPath(path) != nil {
			return errAccountPath
		}
		nonempty := false
		e := filepath.WalkDir(path, func(_ string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() {
				nonempty = true
				return fs.SkipAll
			}
			return nil
		})
		if e != nil && !os.IsNotExist(e) {
			return errAccountPath
		}
		if nonempty && !resume {
			return errPrivateHomeUsed
		}
	}
	owner, err := ownerLoginHost(a)
	if err != nil && !resume {
		return errNoPersonalLogin
	}
	executable := r.harnessExecutable(h, "")
	owner.Executable = executable
	// Use the existing clearing set, while keeping the owner's native roots.
	if err = os.MkdirAll(scope, 0700); err != nil {
		return errAccountPath
	}
	destination, env, err := a.scopedManager(h, scope, executable)
	if err != nil {
		return err
	}
	// An interrupted attempt that wrote no private login copies again.
	if resume {
		if found, err := a.indexOwnerLogin(ctx, h, scope, destination, nil); found || err != nil {
			return err
		}
	}
	source, ok := a.ownerLoginManager(h, owner, env)
	if !ok {
		return native.ErrUnsupported
	}
	var captured []native.Captured
	captured, err = source.Capture(ctx, "")
	if err != nil || len(captured) == 0 {
		if err = loginReadError(err); err != nil {
			return err
		}
		return errNoPersonalLogin
	}
	if err = writePrivateAccountJSON(pending, struct {
		Version int `json:"version"`
	}{1}); err != nil {
		return err
	}
	if err = harnesshome.Ensure(scope); err != nil {
		return errAccountPath
	}
	for _, c := range captured {
		if _, err = destination.Activate(ctx, c.Integration, c.Credential); err != nil {
			return err
		}
	}
	found, err := a.indexOwnerLogin(ctx, h, scope, destination, captured)
	if err == nil && !found {
		err = errCopyFailed
	}
	return err
}

// harnessExecutable returns Dyna's copy of h, or the adapter's binary name
// when no copy is installed.
func (r *Runtime) harnessExecutable(h, version string) string {
	executable := ""
	if adapter, e := Adapter(h); e == nil {
		executable = adapter.Binary
	}
	if r.Bundles != nil {
		if b, e := r.Bundles.Resolve(h, version); e == nil {
			executable = b.Executable
		}
	}
	return executable
}

// ownerLoginManager reads the owner's native login. It also clears each
// selector that the private login environment env clears, while it keeps the
// owner's native roots.
func (a *Accounts) ownerLoginManager(h string, owner native.Host, env map[string]string) (native.Manager, bool) {
	factory := a.NativeManager
	if factory == nil {
		factory = NativeRuntimeManager
	}
	lookup := owner.Env
	owner.Env = func(k string) string {
		if v, ok := env[k]; ok && v == "" {
			return ""
		}
		return lookup(k)
	}
	return factory(h, owner)
}

// indexOwnerLogin saves the imported private login in the account index and
// reports whether the private scope held one. A nil captured list skips the
// source comparison on an interrupted retry.
func (a *Accounts) indexOwnerLogin(ctx context.Context, h, scope string, destination native.Manager, captured []native.Captured) (bool, error) {
	actual, err := destination.Capture(ctx, "")
	if err != nil || len(actual) == 0 {
		return false, loginReadError(err)
	}
	if len(captured) > 0 {
		for _, c := range captured {
			verified := capturedFor(actual, c.Integration)
			if verified == nil || !sameNativeLogin(*verified, c) {
				return true, nil
			}
		}
	}
	if err = checkAccountFiles(h, scope, true); err != nil {
		return true, errAccountPath
	}
	if err = checkPrivateAccountHome(h, scope, true); err != nil {
		return true, err
	}
	// Reuse the native snapshot/index helpers, including private-origin markers.
	var active string
	err = a.transaction(func(s *store.State) error {
		for _, c := range actual {
			if err := a.saveOutgoing(s, h, c); err != nil {
				return err
			}
			for _, e := range s.Entries {
				// Match saveOutgoing: legacy grant-only entries have no native home.
				if e.NativeHome != "" && e.Harness == h && e.Identity == c.Identity && e.Integration == c.Integration {
					if active == "" {
						active = e.ID
					}
					break
				}
			}
		}
		return nil
	}, true)
	if err != nil {
		return true, err
	}
	return true, a.setActive(h, active)
}

// loginReadError marks a transient read failure, such as a failed native
// command. A missing, broken or unsupported login is a final outcome.
func loginReadError(err error) error {
	if err == nil || errors.Is(err, native.ErrNotSignedIn) || errors.Is(err, native.ErrSchema) || errors.Is(err, native.ErrUnsupported) {
		return nil
	}
	return fmt.Errorf("%w %v", errLoginRead, err)
}

// ownerLoginHost drops inherited private selectors, including symlink aliases.
// Dyna's nested command bridge restores the owner's HOME/XDG environment.
func ownerLoginHost(a *Accounts) (native.Host, error) {
	private := func(path string) bool {
		if path == "" {
			return false
		}
		if IsAccountPath(path) {
			return true
		}
		resolve := func(p string) string {
			var tail []string
			for {
				if resolved, e := filepath.EvalSymlinks(p); e == nil {
					for i := len(tail) - 1; i >= 0; i-- {
						resolved = filepath.Join(resolved, tail[i])
					}
					return resolved
				}
				parent := filepath.Dir(p)
				if parent == p {
					return p
				}
				tail = append(tail, filepath.Base(p))
				p = parent
			}
		}
		root, e := filepath.Abs(a.Root)
		if e != nil {
			return true
		}
		path, e = filepath.Abs(path)
		if e != nil {
			return true
		}
		rel, e := filepath.Rel(resolve(root), resolve(path))
		return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) || private(home) {
		return native.Host{}, native.ErrNotSignedIn
	}
	selectors := map[string]bool{}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_CONFIG_DIR", "GROK_HOME", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR"} {
		selectors[k] = true
	}
	lookup := func(k string) string {
		v := os.Getenv(k)
		if selectors[k] && private(v) {
			v = ""
		}
		if v == "" {
			if k == "PI_CODING_AGENT_DIR" {
				return filepath.Join(home, ".pi", "agent")
			}
			if k == "GROK_HOME" {
				return filepath.Join(home, ".grok")
			}
		}
		return v
	}
	return native.Host{Home: home, Env: lookup}, nil
}
