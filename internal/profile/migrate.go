package profile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"dyna-agent/internal/store"
)

// Migrate upgrades retained profile configuration before services start.
// TODO: legacy/shim remove later — reads version 2 profiles and preserves unknown fields.
func Migrate(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	path = resolved
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("profile configuration must be a regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return err
	}
	if header.Version != 2 {
		return nil
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(doc["profiles"], &rows); err != nil {
		return err
	}
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			return err
		}
		var p Profile
		if err := json.Unmarshal(encoded, &p); err != nil {
			return err
		}
		oldArgs := slices.Clone(p.ExtraArgs)
		p.DeclareOutput()
		if err := p.Validate(); err != nil {
			return fmt.Errorf("invalid profile %q: %w", p.Name, err)
		}
		var output map[string]json.RawMessage
		if raw := row["output"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &output); err != nil {
				return err
			}
		}
		if output == nil {
			output = map[string]json.RawMessage{}
		}
		output["dialect"], _ = json.Marshal(p.Output.Dialect)
		row["output"], err = json.Marshal(output)
		if err != nil {
			return err
		}
		if !slices.Equal(oldArgs, p.ExtraArgs) {
			if len(p.ExtraArgs) == 0 {
				delete(row, "extraArgs")
			} else {
				row["extraArgs"], err = json.Marshal(p.ExtraArgs)
				if err != nil {
					return err
				}
			}
		}
	}
	doc["profiles"], err = json.Marshal(rows)
	if err != nil {
		return err
	}
	doc["version"] = json.RawMessage("3")
	transformed, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	transformed = append(transformed, '\n')
	// Validate the final document before preserving and replacing configuration.
	var check Store
	if err := json.Unmarshal(transformed, &check); err != nil {
		return err
	}
	for _, p := range check.Profiles {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	if err := profileV2Backup(path+".v2.bak", raw); err != nil {
		return err
	}
	return store.WriteJSON(path, json.RawMessage(transformed), info.Mode().Perm())
}

var profileBackupWrite = (*os.File).Write

func profileV2Backup(path string, raw []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	n, err := profileBackupWrite(f, raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// A hard link publishes the complete backup without replacing a competing one.
	err = os.Link(f.Name(), path)
	if errors.Is(err, os.ErrExist) {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fmt.Errorf("profile backup is not a private regular file")
		}
		saved, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(saved, raw) {
			return fmt.Errorf("profile backup conflicts with version 2 configuration")
		}
		return nil
	}
	return err
}
