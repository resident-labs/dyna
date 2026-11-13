package folder

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Export writes adopted configuration. Force replaces configuration files only;
// existing messages, policy, hooks, and UNMANAGED.md subtrees are never replaced.
// The report contains each unmanaged path and the marker's first line.
// Callers must serialize access to this private engine-host folder.
func Export(path string, snapshot Snapshot, force bool) ([]string, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Refuse symlink ancestors as well as symlinks anywhere in an existing tree.
	for p := root; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("export path contains symlink: %s", p)
		}
		if e != nil && !os.IsNotExist(e) {
			return nil, e
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if !force {
		entries, e := os.ReadDir(root)
		if e != nil && !os.IsNotExist(e) {
			return nil, e
		}
		if len(entries) > 0 {
			return nil, fmt.Errorf("export destination is not empty (use force)")
		}
	}
	var unmanaged []string
	var report []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if os.IsNotExist(e) && p == root {
			return nil
		}
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("export refuses symlink %s", p)
		}
		if !d.IsDir() {
			return nil
		}
		marker := filepath.Join(p, "UNMANAGED.md")
		info, markerErr := os.Lstat(marker)
		if markerErr != nil && !os.IsNotExist(markerErr) {
			return markerErr
		}
		if markerErr == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unmanaged marker must be a regular file: %s", marker)
			}
			f, e := os.Open(marker)
			if e != nil {
				return e
			}
			scanner := bufio.NewScanner(f)
			scanner.Scan()
			line := scanner.Text()
			scanErr := scanner.Err()
			f.Close()
			if scanErr != nil {
				return scanErr
			}
			rel, e := filepath.Rel(root, p)
			if e != nil {
				return e
			}
			unmanaged = append(unmanaged, rel)
			report = append(report, rel+": unmanaged: "+line)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return report, err
	}
	blocked := func(p string) bool {
		for _, u := range unmanaged {
			if u == "." || p == u || strings.HasPrefix(p, u+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	files := map[string][]byte{}
	add := func(p string, v any) error {
		if blocked(filepath.Dir(p)) {
			return nil
		}
		if _, ok := files[p]; ok {
			return fmt.Errorf("duplicate export path %s", p)
		}
		b, e := yaml.Marshal(v)
		if e != nil {
			return e
		}
		files[p] = b
		return nil
	}
	if err := add("server.yaml", snapshot.Server); err != nil {
		return report, err
	}
	names := map[string]bool{}
	for _, e := range snapshot.Entries {
		if !safeName(e.Name) || (e.Parent != "" && !safeName(e.Parent)) {
			return report, fmt.Errorf("unsafe entry name %q", e.Name)
		}
		if names[e.Name] {
			return report, fmt.Errorf("ambiguous entry name %q", e.Name)
		}
		names[e.Name] = true
		dir := filepath.Join("channels", e.Parent, e.Name)
		if (e.Category == nil) == (e.Channel == nil) {
			return report, fmt.Errorf("entry %q must be category or channel", e.Name)
		}
		if e.Category != nil {
			if e.Parent != "" {
				return report, fmt.Errorf("nested category")
			}
			err = add(filepath.Join(dir, "category.yaml"), e.Category)
		} else {
			err = add(filepath.Join(dir, "channel.yaml"), e.Channel)
		}
		if err != nil {
			return report, err
		}
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// Validate all destinations before creating any files.
	for _, p := range paths {
		dest := filepath.Join(root, p)
		info, e := os.Lstat(dest)
		if e == nil {
			if !force {
				return report, fmt.Errorf("export destination exists: %s (use force)", p)
			}
			if !info.Mode().IsRegular() {
				return report, fmt.Errorf("not a regular file: %s", p)
			}
		} else if !os.IsNotExist(e) {
			return report, e
		}
	}
	for _, p := range paths {
		dest := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return report, err
		}
		f, e := os.CreateTemp(filepath.Dir(dest), ".export-*")
		if e != nil {
			return report, e
		}
		tmp := f.Name()
		_, e = f.Write(files[p])
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e == nil {
			e = os.Rename(tmp, dest)
		}
		if e != nil {
			os.Remove(tmp)
			return report, e
		}
	}
	return report, nil
}
