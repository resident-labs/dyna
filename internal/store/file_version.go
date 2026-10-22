package store

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// FileVersion identifies a file snapshot. Change time distinguishes rapid
// atomic replacements that reuse an inode and preserve size and modification time.
type FileVersion struct {
	device, inode uint64
	size          int64
	modified      time.Time
	changed       int64
	cacheable     bool
}

func VersionOf(info os.FileInfo) FileVersion {
	version := FileVersion{size: info.Size(), modified: info.ModTime()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		version.device = uint64(stat.Dev)
		version.inode = stat.Ino
		version.changed, version.cacheable = ChangeTime(stat)
	}
	return version
}

// Equal reports whether two snapshots can safely share cached data. Platforms
// without inode change time must read again instead of trusting an old snapshot.
func (v FileVersion) Equal(other FileVersion) bool {
	return v.cacheable && other.cacheable && v == other
}

// Key is the version as text, for storing beside a copy of the file. It is
// empty when the platform cannot tell rapid replacements apart, so such a
// copy never counts as current.
func (v FileVersion) Key() string {
	if !v.cacheable {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", v.device, v.inode, v.size, v.modified.UnixNano(), v.changed)
}
