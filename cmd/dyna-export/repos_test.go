package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureManifestRejectsAmbiguousAndUnsafeEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.json")
	for _, content := range []string{
		`{"repositories":[{"path":"/a","identity":"github.com/a/b","defaultRoom":"room"},{"path":"/a","identity":"github.com/c/d","defaultRoom":"room"}],"archiveRefs":[],"skills":[]}`,
		`{"repositories":[{"path":"/a","identity":"https://user:secret@github.com/a/b","defaultRoom":"room"}],"archiveRefs":[],"skills":[]}`,
		`{"repositories":[],"archiveRefs":[{"session":"ses_a","ref":"refs/dyna/archive/other"}],"skills":[]}`,
		`{"repositories":[],"archiveRefs":[],"skills":[{"name":"x","path":"../SKILL.md"}]}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRepoManifest(path); err == nil {
			t.Fatalf("accepted invalid manifest")
		}
	}
}
