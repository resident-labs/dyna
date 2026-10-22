//go:build linux

package computers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Hash file names and contents, including WAL files. Missing paths have a stable
// hash too, so deletions count as changes. Never follow profile symlinks.
func browserStateHash(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			fmt.Fprintf(h, "dir:%s\x00", rel)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("browser state is not a regular file: %s", path)
		}
		fmt.Fprintf(h, "file:%s:%d\x00", rel, info.Size())
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(h, f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
	return fmt.Sprintf("%x", h.Sum(nil)), err
}

func browserProfileHashes(root string) (map[string]string, error) {
	hashes := make(map[string]string)
	names := append(append([]string{}, browserLoginFiles...), browserStorageDirs...)
	for _, name := range names {
		path := filepath.Join(root, "Default", name)
		hash, err := browserStateHash(path)
		if err != nil {
			return nil, err
		}
		if name != "Local Storage" && name != "IndexedDB" {
			wal, err := browserStateHash(path + "-wal")
			if err != nil {
				return nil, err
			}
			hash += wal
		}
		hashes[name] = hash
	}
	return hashes, nil
}

// The caller holds withBrowserMaster and has stopped Chromium. Build a new
// master from the current master, so failed merges cannot discard saved state.
func mergeBrowserProfile(ctx context.Context, src string, lease browserLease) error {
	hashes, err := browserProfileHashes(src)
	if err != nil {
		return err
	}
	changed := false
	for name, hash := range hashes {
		if lease.Baseline[name] != hash {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return stageBrowserProfile(browserMaster(), func(staged string) error {
		if err := copyBrowserProfile(ctx, browserMaster(), "Default", staged, true); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(staged, "Local State")); os.IsNotExist(err) {
			if err := copyBrowserFile(filepath.Join(src, "Local State"), filepath.Join(staged, "Local State")); err != nil {
				return err
			}
		}
		for name, hash := range hashes {
			if lease.Baseline[name] == hash {
				continue
			}
			from, to := filepath.Join(src, "Default", name), filepath.Join(staged, "Default", name)
			switch name {
			case "Cookies", "Network/Cookies":
				if err := mergeBrowserDatabase(ctx, from, to, "cookies", "last_update_utc"); err != nil {
					return err
				}
			case "Login Data":
				if err := mergeBrowserDatabase(ctx, from, to, "logins", "date_password_modified"); err != nil {
					return err
				}
			default:
				size, err := browserTreeSize(from)
				if err != nil {
					return err
				}
				if name != "Web Data" && size > smallBrowserStorage {
					continue
				}
				if err := os.RemoveAll(to); err != nil {
					return err
				}
				if err := os.Remove(to + "-wal"); err != nil && !os.IsNotExist(err) {
					return err
				}
				if name == "Web Data" {
					if err := snapshotBrowserDatabase(ctx, from, to); err != nil {
						return err
					}
				} else if err := copyBrowserTree(from, to); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func browserSQLQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
func browserSQLIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// browserSQLite runs one sqlite3 command. Only the caller's context stops it:
// a merge can run sqlite3 several times, and on a loaded machine one run can
// pass any fixed limit. SQLite's own .timeout bounds lock waits.
func browserSQLite(ctx context.Context, path, sql string) ([]byte, error) {
	out, err := combinedOwned(exec.CommandContext(ctx, "sqlite3", "-bail", "-json", "-cmd", ".timeout 2000", path, sql))
	if err != nil {
		return nil, fmt.Errorf("merge Chromium database: %w: %s", err, out)
	}
	return out, nil
}

func mergeBrowserDatabase(ctx context.Context, src, dst, table, updated string) error {
	if _, err := os.Lstat(src); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(dst); os.IsNotExist(err) {
		return snapshotBrowserDatabase(ctx, src, dst)
	} else if err != nil {
		return err
	}
	// Inspect the actual unique index instead of assuming Chromium's versioned key.
	uri := (&url.URL{Scheme: "file", Path: src, RawQuery: "mode=ro"}).String()
	attach := "ATTACH DATABASE " + browserSQLQuote(uri) + " AS incoming;"
	var indexes []struct {
		Name    string
		Unique  int
		Partial int
	}
	out, err := browserSQLite(ctx, dst, "PRAGMA index_list("+browserSQLQuote(table)+");")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &indexes); err != nil {
		return err
	}
	var keys []string
	for _, index := range indexes {
		if index.Unique != 1 || index.Partial != 0 {
			continue
		}
		var cols []struct{ Name *string }
		out, err := browserSQLite(ctx, dst, "PRAGMA index_info("+browserSQLQuote(index.Name)+");")
		if err != nil {
			return err
		}
		if err := json.Unmarshal(out, &cols); err != nil {
			return err
		}
		for _, col := range cols {
			if col.Name == nil {
				return fmt.Errorf("unsupported expression index in %s", table)
			}
			keys = append(keys, *col.Name)
		}
		break
	}
	if len(keys) == 0 {
		return fmt.Errorf("no unique index in Chromium %s", table)
	}
	var cols []struct {
		Name string
		PK   int
	}
	out, err = browserSQLite(ctx, dst, "PRAGMA table_info("+browserSQLQuote(table)+");")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &cols); err != nil {
		return err
	}
	var sourceCols []struct{ Name string }
	out, err = browserSQLite(ctx, dst, attach+"PRAGMA incoming.table_info("+browserSQLQuote(table)+");")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(out, &sourceCols); err != nil {
		return err
	}
	sourceNames := make(map[string]bool)
	for _, col := range sourceCols {
		sourceNames[col.Name] = true
	}
	var names, updates, conflict []string
	hasUpdated := false
	for _, key := range keys {
		conflict = append(conflict, browserSQLIdent(key))
	}
	for _, col := range cols {
		key := false
		for _, k := range keys {
			if k == col.Name {
				key = true
			}
		}
		// Login IDs are local to each database. Let SQLite allocate new IDs.
		if col.PK != 0 && !key {
			continue
		}
		if !sourceNames[col.Name] {
			return fmt.Errorf("incompatible Chromium %s schema: missing %s", table, col.Name)
		}
		q := browserSQLIdent(col.Name)
		names = append(names, q)
		if !key {
			updates = append(updates, q+"=excluded."+q)
		}
		if col.Name == updated {
			hasUpdated = true
		}
	}
	if !hasUpdated {
		return fmt.Errorf("missing Chromium timestamp %s", updated)
	}
	list := strings.Join(names, ",")
	sql := attach + "BEGIN IMMEDIATE; INSERT INTO " + browserSQLIdent(table) + " (" + list + ") SELECT " + list + " FROM incoming." + browserSQLIdent(table) + " WHERE true ON CONFLICT (" + strings.Join(conflict, ",") + ") DO UPDATE SET " + strings.Join(updates, ",") + " WHERE excluded." + browserSQLIdent(updated) + ">" + browserSQLIdent(table) + "." + browserSQLIdent(updated) + "; COMMIT;"
	_, err = browserSQLite(ctx, dst, sql)
	return err
}
