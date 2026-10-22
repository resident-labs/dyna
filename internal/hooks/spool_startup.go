package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	startupSpoolEntries = 4096
	startupSpoolFile    = 1 << 20
	startupSpoolTotal   = 16 << 20
)

// validateStartupSpool examines pending records without changing the spool.
// TODO: legacy/shim remove later — refuses old snapshot-owned queue records; remove once the approved cutover proves all old emitters stopped and all supported spools drained.
func (s Service) validateStartupSpool() error {
	path := s.spoolDir()
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("validate hook spool %s: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("validate hook spool %s: not a physical directory", path)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return fmt.Errorf("validate hook spool %s: %w", path, err)
	}
	defer root.Close()
	dir, err := root.OpenFile(".", os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("validate hook spool %s: %w", path, err)
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil {
		return fmt.Errorf("validate hook spool %s: %w", path, err)
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("validate hook spool %s: directory changed while opening", path)
	}
	seen, used := 0, 0
	for {
		entries, readErr := dir.ReadDir(min(128, startupSpoolEntries-seen+1))
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("validate hook spool %s: %w", path, readErr)
		}
		for _, entry := range entries {
			seen++
			if seen > startupSpoolEntries {
				return fmt.Errorf("validate hook spool %s: exceeds %d entries", path, startupSpoolEntries)
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			if _, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64); err != nil {
				continue
			}
			source := filepath.Join(path, name)
			raw, err := readStartupSpoolFile(root, name, min(startupSpoolFile, startupSpoolTotal-used))
			if err != nil {
				return fmt.Errorf("validate hook spool %s: %w", source, err)
			}
			used += len(raw)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				return fmt.Errorf("validate hook spool %s: %w", source, err)
			}
			if fields == nil {
				return fmt.Errorf("validate hook spool %s: event is not an object", source)
			}
			var event Event
			if err := json.Unmarshal(raw, &event); err != nil {
				return fmt.Errorf("validate hook spool %s: %w", source, err)
			}
			marker, present := fields["snapshot"]
			if !present {
				continue
			}
			switch string(bytes.TrimSpace(marker)) {
			case "false":
				continue
			case "true":
				return fmt.Errorf("validate hook spool %s: retained snapshot requires the approved old-emitter drain before startup", source)
			default:
				return fmt.Errorf("validate hook spool %s: snapshot marker is not a boolean", source)
			}
		}
		if readErr == io.EOF || len(entries) == 0 {
			return nil
		}
	}
}

// readStartupSpoolFile requires a physical regular file through the pinned root.
// The opened file must match that physical file before any bytes are read.
func readStartupSpoolFile(root *os.Root, name string, limit int) ([]byte, error) {
	physical, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !physical.Mode().IsRegular() {
		return nil, fmt.Errorf("not a physical regular file")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if !os.SameFile(physical, info) {
		return nil, fmt.Errorf("file changed while opening")
	}
	if info.Size() > int64(limit) {
		return nil, fmt.Errorf("exceeds remaining validation budget of %d bytes", limit)
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("exceeds remaining validation budget of %d bytes", limit)
	}
	return raw, nil
}
