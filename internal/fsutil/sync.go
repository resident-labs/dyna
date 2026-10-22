package fsutil

import "os"

// SyncDir flushes a directory's entries, so a create, rename or remove in it
// survives a crash. File contents are made durable by their own Sync; this is
// the second half of a durable rename.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
