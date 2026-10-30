package workflowsvc

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/profile"
)

func TestReplayFingerprintIncludesNestedSourcesAndInputs(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child.js")
	if err := os.WriteFile(child, []byte(`return 'one'`), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{ScriptSrc: `const meta = { deterministic: true }; return await workflow('./child.js');`, WorkDir: dir}
	store := &profile.Store{}
	before, _, err := replayFingerprint(opts, store, []string{"HOME=/one"}, "head")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte(`return 'two'`), 0600); err != nil {
		t.Fatal(err)
	}
	after, _, err := replayFingerprint(opts, store, []string{"HOME=/one"}, "head")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("nested source change did not change fingerprint")
	}
	other, _, err := replayFingerprint(opts, store, []string{"HOME=/two"}, "head")
	if err != nil {
		t.Fatal(err)
	}
	if after == other {
		t.Fatal("environment change did not change fingerprint")
	}
	withID, _, err := replayFingerprint(opts, store, []string{"HOME=/one", "DYNA_RUN_ID=wf_first"}, "head")
	if err != nil {
		t.Fatal(err)
	}
	restarted, _, err := replayFingerprint(opts, store, []string{"HOME=/one", "DYNA_RUN_ID=wf_next"}, "head")
	if err != nil {
		t.Fatal(err)
	}
	if withID != restarted {
		t.Fatal("attempt identity changed fingerprint")
	}
}

func TestReplayFingerprintRejectsChangedRootSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "root.js")
	if err := os.WriteFile(path, []byte(`return 1`), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{ScriptSrc: `return 1`, ScriptPath: path, WorkDir: dir}
	if _, _, err := replayFingerprint(opts, &profile.Store{}, nil, "head"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`return 2`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := replayFingerprint(opts, &profile.Store{}, nil, "head"); err == nil {
		t.Fatal("accepted changed root source")
	}
}

func TestReplayFingerprintRejectsAmbiguousEnvironment(t *testing.T) {
	opts := Options{ScriptSrc: `return 1`, WorkDir: t.TempDir()}
	if _, _, err := replayFingerprint(opts, &profile.Store{}, []string{"VALUE=one", "VALUE=two"}, "head"); err == nil {
		t.Fatal("accepted duplicate environment values")
	}
}
