package home

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dyna-agent/internal/store"
)

// OwnerEnvKey carries the owner's roots into a private harness launch, so every
// dyna process started there can restore them itself.
const OwnerEnvKey = "DYNA_OWNER_ENV"

// ownerRestoreKeys are the settings RestoreOwner sets back. A key missing from
// the record was absent for the owner and is unset.
var ownerRestoreKeys = append([]string{"HOME", "GIT_CONFIG_GLOBAL", "DYNA_BIN"}, xdgNames...)

// bundleWriteCommands change the harness bundle store directly, in this
// process or through the engine: install, login, rm, rollback, gc.
var bundleWriteCommands = []string{"install", "login", "rm", "rollback", "gc"}

type ownerEnv struct {
	// Scope is the native scope whose private roots Prepare set.
	Scope string            `json:"scope"`
	Env   map[string]string `json:"env"`
}

func ownerRecord(nativeScope, realDyna string, owner map[string]string) (string, error) {
	env := map[string]string{"DYNA_BIN": realDyna}
	for _, key := range ownerRestoreKeys {
		if value, ok := owner[key]; ok && key != "DYNA_BIN" {
			env[key] = value
		}
	}
	record, err := json.Marshal(ownerEnv{Scope: nativeScope, Env: env})
	return string(record), err
}

// RestoreOwner gives a dyna process the owner's HOME, XDG roots, Git config and
// real dyna binary when it starts inside a private harness HOME. It covers every
// way of starting dyna: PATH, $DYNA_BIN, an absolute path, a hook or a
// generated wrapper. It also drops private bridge directories from PATH, so a
// nested Prepare resolves owner tools. It changes nothing when the caller set a
// HOME or XDG root that is neither private nor the owner's — except that a
// bundle-writing harness command then refuses rather than write the owner's
// live store — and for `dyna mcp serve`, whose generated servers read
// private discovery roots.
func RestoreOwner(args []string) error {
	raw, ok := os.LookupEnv(OwnerEnvKey)
	if !ok || KeepsPrivateRoots(args) {
		return nil
	}
	launch := os.Environ()
	_ = os.Unsetenv(OwnerEnvKey)
	var record ownerEnv
	if err := json.Unmarshal([]byte(raw), &record); err != nil || !filepath.IsAbs(record.Scope) || record.Env["DYNA_BIN"] == "" {
		return fmt.Errorf("invalid %s", OwnerEnvKey)
	}
	// Each root must still be private or already the owner's, as after a Git or
	// tool bridge. Any other value is the caller's own choice.
	var callerRoots []string
	for key, private := range Roots(record.Scope) {
		current, set := os.LookupEnv(key)
		owner, ownerSet := record.Env[key]
		if current != private && (set != ownerSet || current != owner) {
			callerRoots = append(callerRoots, key)
		}
	}
	if len(callerRoots) > 0 {
		// A bundle-writing command in a shell whose caller chose HOME or
		// XDG_DATA_HOME would write the owner's live store while the caller's
		// roots point somewhere else (D-398). The caller picks a scratch
		// DYNA_DATA_DIR for tests, or drops its changed roots.
		if os.Getenv(store.DataDirEnv) == "" &&
			(slices.Contains(callerRoots, "HOME") || slices.Contains(callerRoots, "XDG_DATA_HOME")) &&
			len(args) >= 2 && args[0] == "harness" && slices.Contains(bundleWriteCommands, args[1]) {
			return fmt.Errorf("%s %s would write the owner's live bundle store from a shell with its own HOME or XDG_DATA_HOME; set DYNA_DATA_DIR to a scratch folder for tests, or unset the changed roots to act on the owner's store", args[0], args[1])
		}
		return keepOwnerData(args, record, callerRoots)
	}
	for _, key := range ownerRestoreKeys {
		var err error
		if value, ok := record.Env[key]; ok {
			err = os.Setenv(key, value)
		} else {
			err = os.Unsetenv(key)
		}
		if err != nil {
			return err
		}
	}
	bridges := filepath.Join(Home(record.Scope), ".dyna") + string(filepath.Separator)
	var path []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if !strings.HasPrefix(dir, bridges) {
			path = append(path, dir)
		}
	}
	launchEnv = launch
	return os.Setenv("PATH", strings.Join(path, string(os.PathListSeparator)))
}

// keepOwnerData runs when the caller chose its own HOME or XDG roots
// (callerRoots), for example a game helper that sets fresh ones before it runs
// dyna. The caller keeps them, and so do its children. A caller that set a
// HOME of its own while inheriting XDG_DATA_HOME — the harness's private root
// or the owner's — loses that inherited root, so every command, `dyna engine
// run` included, resolves the data folder under the caller's home instead of
// the owner's live store (D-398). A dyna client command still needs the
// owner's data folder to reach the engine, so it gets that through
// store.DataDirEnv alone. `dyna engine run` may serve any folder the caller
// picked, and a value the caller already set stays.
func keepOwnerData(args []string, record ownerEnv, callerRoots []string) error {
	if os.Getenv(store.DataDirEnv) != "" {
		return nil
	}
	if slices.Contains(callerRoots, "HOME") && !slices.Contains(callerRoots, "XDG_DATA_HOME") {
		if err := os.Unsetenv("XDG_DATA_HOME"); err != nil {
			return err
		}
	}
	if len(args) == 0 || args[0] == "engine" {
		return nil
	}
	data := record.Env["XDG_DATA_HOME"]
	if data == "" && record.Env["HOME"] != "" {
		data = filepath.Join(record.Env["HOME"], ".local", "share")
	}
	if !filepath.IsAbs(data) {
		return nil
	}
	return os.Setenv(store.DataDirEnv, filepath.Join(data, "dyna"))
}

// KeepsPrivateRoots reports whether a dyna command runs with the harness's
// private roots on purpose: `dyna mcp serve`, whose generated servers read
// private discovery roots.
func KeepsPrivateRoots(args []string) bool {
	return len(args) >= 2 && args[0] == "mcp" && args[1] == "serve"
}

// launchEnv is this process's environment before RestoreOwner changed it.
var launchEnv []string

// LaunchEnv is the environment this dyna process started with. Commands that
// stand in for the harness's own, such as shell jobs, run with it, so they
// keep the harness's private HOME, roots and PATH bridges.
func LaunchEnv() []string {
	if launchEnv != nil {
		return slices.Clone(launchEnv)
	}
	return os.Environ()
}
