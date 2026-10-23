package sessionlife

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// TODO: legacy/shim remove later — retain session.json input only until all
// installations have completed the one-time sessions.import migration.
const legacyName = "session.json"

// legacyLoadRaw reads a session.json written before the record moved into
// dyna.db, for the startup importer only. The read stays inside sessions:
// no symlink may lead outside sessions, and the file must be private,
// regular and one JSON object.
func legacyLoadRaw(sessions, id string) ([]byte, error) {
	root, err := os.OpenRoot(sessions)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Join(id, legacyName), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%w: session %s record is a symlink", ErrRecord, id)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission) {
		// os.Root reports a path that escapes sessions; never read past it.
		return nil, fmt.Errorf("%w: session %s: %v", ErrRecord, id, err)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > recordLimit {
		return nil, fmt.Errorf("%w: session %s record is not a private regular file", ErrRecord, id)
	}
	raw, err := io.ReadAll(io.LimitReader(f, recordLimit+1))
	if err != nil {
		return nil, err
	}
	if !isObject(raw) {
		return nil, fmt.Errorf("%w: session %s record is not a JSON object", ErrRecord, id)
	}
	return raw, nil
}
