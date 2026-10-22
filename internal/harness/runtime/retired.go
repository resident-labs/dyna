package runtime

import (
	"os"
	"path/filepath"
	"slices"

	"dyna-agent/internal/account/store"
)

// TODO: legacy/shim remove later — Cursor (cursor-agent) was removed on 2026-10-18; gc drops its account records and deletes Dyna's leftover bundle and account folders, until no data directory still has them
var retiredHarnesses = []string{
	"cursor-agent",
	// TODO: legacy/shim remove later — qwen-audio-agent, the old voice gateway, was replaced by the Go voice service on 2026-10-19; gc deletes its leftover bundle folder, until no data directory still has one
	"qwen-audio-agent",
}

func retiredHarness(h string) bool { return slices.Contains(retiredHarnesses, h) }

// RemoveRetiredHarnesses drops account records for harnesses Dyna no longer
// supports, then deletes Dyna's own bundle and account folders for them. Other
// commands skip such records, so this is the one path that removes them. It
// never touches the owner's personal harness files outside Dyna's data.
func RemoveRetiredHarnesses(accounts *Accounts, bundles *BundleManager) error {
	err := (&store.Store{Root: accounts.Root}).Update(func(st *store.State) error {
		st.Entries = slices.DeleteFunc(st.Entries, func(e store.Entry) bool { return retiredHarness(e.Harness) })
		return nil
	})
	if err != nil {
		return err
	}
	for _, h := range retiredHarnesses {
		for _, dir := range []string{filepath.Join(bundles.Root, h), filepath.Join(accounts.Root, h)} {
			if _, err := os.Lstat(dir); os.IsNotExist(err) {
				continue
			}
			if err := removeBundle(dir); err != nil {
				return err
			}
		}
	}
	return nil
}
