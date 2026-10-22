package home

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// publish keeps complete, immutable sets until their paired HOME is removed.
// The hash covers a format version and generated file bytes. The file names
// encode the effective tool list; their contents encode restored owner settings
// and resolved tool paths. Unrelated session stamps and credentials do not
// create more retained sets or contribute secret values to their hashes.
func publish(root string, files map[string][]byte) (string, error) {
	input := struct {
		Version int
		Files   map[string][]byte
	}{2, files}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	unlock, err := lockPublication(root)
	if err != nil {
		return "", err
	}
	defer unlock()
	sets := filepath.Join(root, "tool-home")
	if err := privateDir(sets); err != nil {
		return "", err
	}
	final := filepath.Join(sets, digest)
	if _, err := os.Lstat(final); err == nil {
		if err := verifyPublication(final, files); err != nil {
			return "", fmt.Errorf("immutable publication failed validation: %w; stop launches using this set, remove the entire publication directory %q, and retry", err, final)
		}
		return final, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	stage, err := os.MkdirTemp(sets, ".publish-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for name, contents := range files {
		if err := atomicFile(filepath.Join(stage, name), contents, publicationMode(name)); err != nil {
			return "", err
		}
	}
	if err := os.Rename(stage, final); err != nil {
		return "", err
	}
	return final, nil
}

func publicationMode(name string) os.FileMode {
	if name == "gitconfig" {
		return 0600
	}
	return 0700
}

// Check expected paths without traversing tool caches. A damaged publication is
// rejected rather than repaired: a running process can still refer to its files.
func verifyPublication(root string, files map[string][]byte) error {
	for _, dir := range []string{root, filepath.Join(root, "bin")} {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return fmt.Errorf("unsafe immutable bridge directory %q", dir)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "bin"))
	if err != nil {
		return err
	}
	if len(entries) != len(files)-1 {
		return fmt.Errorf("immutable bridge tool list changed: %q", root)
	}
	for _, entry := range entries {
		if _, ok := files["bin/"+entry.Name()]; !ok {
			return fmt.Errorf("unexpected immutable bridge tool %q", entry.Name())
		}
	}
	for name, expected := range files {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != publicationMode(name) {
			return fmt.Errorf("unsafe immutable bridge file %q", path)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(contents, expected) {
			return fmt.Errorf("immutable bridge file changed: %q", path)
		}
	}
	return nil
}
