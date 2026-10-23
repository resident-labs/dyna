package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type diskIndex struct {
	State
	// TODO: legacy/shim remove later — reads the old mixed quota until all saved indexes use scoped entries.
	LegacyRate *RateLimit        `json:"rateLimit,omitempty"`
	Format     int               `json:"version"`
	Files      map[string]string `json:"recordFiles"`
	Closed     string            `json:"closedIndex,omitempty"`
}

func newState() State {
	return State{Version: 1, Records: map[string]*Record{}, Reviewers: map[string]Reviewer{}, NoiseRules: map[string]NoiseRule{}, RateLimits: map[string]RateLimit{}}
}

// TODO: legacy/shim remove later — reads the JSON PR store (index.json, records/, closed/, or the version-1 state.json) once to fill dyna.db; remove when no supported store still holds only JSON.
// found is false when the folder holds no PR state. The files are never changed.
func readLegacy(root string) (st legacyDocuments, found bool, err error) {
	st.records = map[string]json.RawMessage{}
	path := filepath.Join(root, "index.json")
	defer func() {
		if err != nil {
			err = fmt.Errorf("PR import %s: %w", path, err)
		}
	}()
	if _, err = os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join(root, "state.json")
		if _, err = os.Stat(path); os.IsNotExist(err) {
			return st, false, nil
		}
	}
	if err != nil {
		return st, false, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return st, false, err
	}
	st.source = path
	// A missing version is a version-1 whole-file store.
	disk := diskIndex{State: newState(), Format: 1}
	if err = json.Unmarshal(b, &disk); err != nil {
		return st, false, fmt.Errorf("invalid PR state: %w", err)
	}
	if disk.Files == nil && disk.Format == 2 {
		// The fence that keeps old binaries off the store holds no state.
		return st, false, nil
	}
	combined := disk.Files != nil && disk.Format == 2
	if (disk.Files == nil && disk.Format != 1) || (disk.Files != nil && !combined && disk.Format != 3) {
		return st, false, fmt.Errorf("unsupported PR state version %d", disk.Format)
	}
	if err := json.Unmarshal(b, &st.index); err != nil {
		return st, false, err
	}
	if st.index == nil {
		st.index = map[string]json.RawMessage{}
	}
	if disk.Files == nil {
		var inline, responses map[string]json.RawMessage
		if raw := st.index["records"]; raw != nil {
			if err := json.Unmarshal(raw, &inline); err != nil {
				return st, false, err
			}
		}
		if raw := st.index["httpResponses"]; raw != nil {
			if err := json.Unmarshal(raw, &responses); err != nil {
				return st, false, err
			}
		}
		for key, body := range inline {
			row := map[string]json.RawMessage{"record": body}
			if responses[key] != nil {
				row["responses"] = responses[key]
			}
			raw, err := json.Marshal(row)
			if err != nil {
				return st, false, err
			}
			st.records[key] = raw
		}
	}
	files := map[string]string{}
	origins := map[string]string{}
	for key, name := range disk.Files {
		files[key] = name
		origins[key] = path
	}
	if disk.Format == 3 && disk.Closed != "" {
		if filepath.Base(disk.Closed) != disk.Closed || !strings.HasSuffix(disk.Closed, ".json") {
			return st, false, fmt.Errorf("invalid closed PR index path %q", disk.Closed)
		}
		closedPath := filepath.Join(root, "closed", disk.Closed)
		raw, err := os.ReadFile(closedPath)
		if err != nil {
			return st, false, err
		}
		var closedFiles map[string]string
		if err = json.Unmarshal(raw, &closedFiles); err != nil {
			return st, false, fmt.Errorf("invalid closed PR index %s: %w", closedPath, err)
		}
		for key, name := range closedFiles {
			if _, duplicate := files[key]; duplicate {
				return st, false, fmt.Errorf("PR %s occurs in both indexes", key)
			}
			files[key] = name
			origins[key] = closedPath
		}
	}
	if disk.Files != nil {
		for key, name := range files {
			if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
				return st, false, fmt.Errorf("invalid PR record path %q in %s", name, origins[key])
			}
			raw, err := os.ReadFile(filepath.Join(root, "records", name))
			if err != nil {
				return st, false, err
			}
			var record diskRecord
			if err = json.Unmarshal(raw, &record); err != nil {
				return st, false, fmt.Errorf("invalid PR record %s: %w", filepath.Join(root, "records", name), err)
			}
			st.records[key] = raw
		}
	}
	for _, field := range []string{"records", "httpResponses", "recordFiles", "closedIndex"} {
		delete(st.index, field)
	}
	st.index["version"] = json.RawMessage("1")
	return st, true, nil
}
