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
func readLegacy(root string) (st State, found bool, err error) {
	st = newState()
	path := filepath.Join(root, "index.json")
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
	// A missing version is a version-1 whole-file store.
	disk := diskIndex{State: st, Format: 1}
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
	st = disk.State
	if disk.LegacyRate != nil {
		st.ImportLegacyQuota(*disk.LegacyRate)
	}
	files := map[string]string{}
	for key, name := range disk.Files {
		files[key] = name
	}
	if disk.Format == 3 && disk.Closed != "" {
		if filepath.Base(disk.Closed) != disk.Closed || !strings.HasSuffix(disk.Closed, ".json") {
			return st, false, fmt.Errorf("invalid closed PR index path %q", disk.Closed)
		}
		raw, err := os.ReadFile(filepath.Join(root, "closed", disk.Closed))
		if err != nil {
			return st, false, err
		}
		var closedFiles map[string]string
		if err = json.Unmarshal(raw, &closedFiles); err != nil {
			return st, false, fmt.Errorf("invalid closed PR index: %w", err)
		}
		for key, name := range closedFiles {
			if _, duplicate := files[key]; duplicate {
				return st, false, fmt.Errorf("PR %s occurs in both indexes", key)
			}
			files[key] = name
		}
	}
	if disk.Files != nil {
		st.Records = map[string]*Record{}
		st.Responses = map[string]map[string]CachedResponse{}
		for key, name := range files {
			if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
				return st, false, fmt.Errorf("invalid PR record path %q", name)
			}
			raw, err := os.ReadFile(filepath.Join(root, "records", name))
			if err != nil {
				return st, false, err
			}
			var record diskRecord
			if err = json.Unmarshal(raw, &record); err != nil {
				return st, false, fmt.Errorf("invalid PR record %s: %w", key, err)
			}
			st.Records[key] = record.Record
			if record.Responses != nil {
				st.Responses[key] = record.Responses
			}
		}
	}
	normalizeState(&st)
	return st, true, nil
}
