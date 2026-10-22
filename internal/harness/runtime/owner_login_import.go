package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
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
	errSQLiteMissing   = errors.New("sqlite3 is missing.")
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
	if h != "opencode" {
		captured, err = source.Capture(ctx, "")
		if err != nil || len(captured) == 0 {
			if err = loginReadError(err); err != nil {
				return err
			}
			return errNoPersonalLogin
		}
	}
	if err = writePrivateAccountJSON(pending, struct {
		Version int `json:"version"`
	}{1}); err != nil {
		return err
	}
	if err = harnesshome.Ensure(scope); err != nil {
		return errAccountPath
	}
	if h == "opencode" {
		// Native Capture initializes the schema. Never activate an owner v2-ref.
		rows, e := destination.Capture(ctx, "")
		if e != nil && !errors.Is(e, native.ErrNotSignedIn) {
			if e = loginReadError(e); e != nil {
				return e
			}
			return errPrivateHomeUsed
		}
		if len(rows) != 0 {
			return errPrivateHomeUsed
		}
		if err = importOwnerOpenCodeRows(ctx, owner, filepath.Join(scope, "opencode.db")); err != nil {
			retry = errors.Is(err, errOwnerLoginBusy)
			return err
		}
	} else {
		for _, c := range captured {
			if _, err = destination.Activate(ctx, c.Integration, c.Credential); err != nil {
				return err
			}
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
	if h != "opencode" {
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
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_CONFIG_DIR", "CODEX_HOME", "CODEX_SQLITE_HOME", "MUSE_AUTH_PATH", "GROK_HOME", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR", "OPENCODE_CONFIG_DIR", "OPENCODE_DB", "OPENCODE_TEST_HOME", "OPENCODE_CONFIG"} {
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

// The inspected v2 CLI has no auth export/import command. Its credential table
// has these nine columns (verified on the installed v2 artifact, 2026-10-18).
// SQLite reads the owner's live WAL through a read-only connection. Only login
// rows enter the initialized private DB; sessions/settings never leave owner DB.
func importOwnerOpenCodeRows(ctx context.Context, owner native.Host, destination string) error {
	data := owner.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(owner.HomeDir(), ".local", "share")
	}
	source := owner.Getenv("OPENCODE_DB")
	if source == "" {
		source = "opencode-v2.db"
		if v := owner.Getenv("OPENCODE_DISABLE_CHANNEL_DB"); v == "1" || v == "true" {
			source = "opencode.db"
		}
	}
	if !filepath.IsAbs(source) {
		source = filepath.Join(data, "opencode", source)
	}
	if safeAccountPath(source) != nil || checkPrivateAccountFile(destination, false) != nil {
		return errAccountPath
	}
	if err := chmodAccountNode(destination, 0600, false); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || source == destination {
		return native.ErrNotSignedIn
	}
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		return errSQLiteMissing
	}
	uri := (&url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro"}).String()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	columns := "id,integration_id,label,value,connector_id,method_id,active,time_created,time_updated"
	// All guards run inside one transaction. A constraint failure rolls it back.
	sql := ".bail on\n.timeout 500\nATTACH DATABASE " + quote(uri) + " AS owner;\nBEGIN IMMEDIATE;\nCREATE TEMP TABLE import_guard (ok INTEGER CHECK(ok=1));\n" +
		"INSERT INTO import_guard SELECT count(*)=0 FROM main.credential;\n" +
		"INSERT INTO import_guard SELECT group_concat(name, ',')=" + quote(columns) + " FROM pragma_table_info('credential','owner');\n" +
		"INSERT INTO import_guard SELECT count(*)<=128 AND coalesce(max(length(value)),0)<=4194304 FROM owner.credential;\n" +
		"INSERT INTO main.credential (" + columns + ") SELECT " + columns + " FROM owner.credential WHERE integration_id IN ('opencode','opencode-go');\nCOMMIT;\n"
	cmd := exec.CommandContext(ctx, sqlite, "-batch", destination)
	cmd.Stdin = strings.NewReader(sql)
	// Keep only the error class. SQLite errors never echo credential values.
	var stderr boundedBuffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	cmd.WaitDelay = time.Second
	if cmd.Run() != nil {
		if msg := stderr.String(); strings.Contains(msg, "database is locked") || strings.Contains(msg, "database is busy") {
			return errOwnerLoginBusy
		}
		return native.ErrSchema
	}
	return chmodAccountNode(destination, 0600, false)
}

// boundedBuffer keeps the first 4 KiB of sqlite3 stderr.
type boundedBuffer struct{ b []byte }

func (w *boundedBuffer) Write(p []byte) (int, error) {
	if room := 4096 - len(w.b); room > 0 {
		w.b = append(w.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
func (w *boundedBuffer) String() string { return string(w.b) }
