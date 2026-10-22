package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ImportLegacyClaude only reads the owner's old helper snapshots. It does not
// run the helper, inspect live credentials, or renew any saved grant. An entry
// without a complete locally bound subscription credential is skipped.
// TODO: legacy/shim remove later — imports old claude-accounts helper snapshots; remove after the supported import window closes
func ImportLegacyClaude(ctx context.Context, h Host) ([]Captured, error) {
	m := &claudeManager{host: h}
	base := h.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(h.HomeDir(), ".local", "state")
	}
	base, err := m.path(base)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(base, "claude-accounts")
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	st, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return []Captured{}, nil
	}
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return nil, claudeFileError()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, claudeFileError()
	}
	captured := []Captured{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !claudeUUID.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		credentials, e := claudeReadObject(filepath.Join(dir, ".credentials.json"))
		if e != nil {
			continue
		}
		account, e := claudeReadObject(filepath.Join(dir, "oauthAccount.json"))
		if e != nil || len(account) == 0 {
			continue
		}
		raw, e := json.Marshal(account)
		if e != nil {
			continue
		}
		accounts, e := claudeCaptured(credentials, map[string]json.RawMessage{"oauthAccount": raw})
		if e != nil || len(accounts) != 1 {
			continue
		}
		accountID := "anthropic:" + strings.ToLower(entry.Name())
		if accounts[0].Identity != accountID || seen[accountID] {
			continue
		}
		// Helper labels are private display metadata. They never enter errors.
		if meta, e := claudeReadObject(filepath.Join(dir, "meta.json")); e == nil {
			var id, label string
			_ = json.Unmarshal(meta["uuid"], &id)
			_ = json.Unmarshal(meta["label"], &label)
			if id != "" && !strings.EqualFold(id, entry.Name()) {
				continue
			}
			if strings.TrimSpace(label) != "" {
				accounts[0].Label = label
			}
		}
		seen[accountID] = true
		captured = append(captured, accounts[0])
	}
	return captured, nil
}
