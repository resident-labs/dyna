// Package storetest gives tests their own Dyna data folder.
package storetest

import (
	"path/filepath"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/store"
)

// Isolate gives a fixture private data, config, scratch and runtime homes,
// with no inherited Dyna identity. It returns the fixture's Dyna data folder.
// The private runtime home means an engine call reaches only a fake engine
// that the test serves (enginetest.Serve), never the owner's live engine.
func Isolate(t testing.TB) string {
	t.Helper()
	root := DataDir(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	// Session scratch uses a disk base with a namespace per store, so a test
	// must redirect the base explicitly.
	t.Setenv("DYNA_SCRATCH_ROOT", filepath.Join(t.TempDir(), "scratch"))
	ClearStamp(t)
	return root
}

// ClearStamp removes the caller stamp while preserving existing fixture homes.
func ClearStamp(t testing.TB) {
	t.Helper()
	for _, key := range mode.StampKeys() {
		t.Setenv(key, "")
	}
}

// DataDir points XDG_DATA_HOME (and clears DYNA_DATA_DIR) at a temporary folder for the rest of the
// test and returns the Dyna data folder inside it. A test then never writes
// to the owner's store, or to a harness runtime home's.
func DataDir(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	// DYNA_DATA_DIR wins over XDG_DATA_HOME, so an inherited one must not stay.
	t.Setenv(store.DataDirEnv, "")
	return filepath.Join(root, "dyna")
}
