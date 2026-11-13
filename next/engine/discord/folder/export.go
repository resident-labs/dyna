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
	unmanaged, report, err := scanExportTree(root, force)
	if err != nil {
		return report, err
	}
	files, err := buildExportFiles(snapshot, unmanaged)
	if err != nil {
		return report, err
	}
	report = append(report, snapshot.Warnings...)
	var generatedPaths []string
	for path := range files {
		if filepath.Base(path) == "UNMANAGED.md" {
			generatedPaths = append(generatedPaths, path)
		}
	}
	sort.Strings(generatedPaths)
	for _, path := range generatedPaths {
		report = append(report, filepath.Dir(path)+": unmanaged: "+strings.TrimSuffix(string(files[path]), "\n"))
	}
	return report, writeExportFiles(root, files, force)
}

// scanExportTree refuses symlinks outside unmanaged directories. It looks for a
// marker before descending, so even unusual unmanaged contents stay opaque.
func scanExportTree(root string, force bool) ([]string, []string, error) {
	for path := root; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, fmt.Errorf("export path contains symlink: %s", path)
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	if !force {
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		if len(entries) > 0 {
			return nil, nil, fmt.Errorf("export destination is not empty (use force)")
		}
	}
	var unmanaged, report []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("export refuses symlink %s", path)
		}
		if !entry.IsDir() {
			return nil
		}
		marker := filepath.Join(path, "UNMANAGED.md")
		info, err := os.Lstat(marker)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unmanaged marker must be a regular file: %s", marker)
		}
		file, err := os.Open(marker)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Scan()
		line := scanner.Text()
		scanErr := scanner.Err()
		file.Close()
		if scanErr != nil {
			return scanErr
		}
		relativePath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		unmanaged = append(unmanaged, relativePath)
		report = append(report, relativePath+": unmanaged: "+line)
		return filepath.SkipDir
	})
	return unmanaged, report, err
}

func buildExportFiles(snapshot Snapshot, unmanaged []string) (map[string][]byte, error) {
	files := map[string][]byte{}
	var generatedDirectories []string
	for _, entry := range snapshot.Entries {
		if entry.Unmanaged != "" {
			generatedDirectories = append(generatedDirectories, filepath.Join("channels", entry.Parent, entry.Name))
		}
	}
	add := func(path string, value any) error {
		directory := filepath.Dir(path)
		for _, unmanagedPath := range unmanaged {
			if unmanagedPath == "." || directory == unmanagedPath || strings.HasPrefix(directory, unmanagedPath+string(filepath.Separator)) {
				return nil
			}
		}
		for _, generatedDirectory := range generatedDirectories {
			if strings.HasPrefix(directory, generatedDirectory+string(filepath.Separator)) {
				return nil
			}
		}
		if _, exists := files[path]; exists {
			return fmt.Errorf("duplicate export path %s", path)
		}
		if filepath.Base(path) == "UNMANAGED.md" {
			files[path] = []byte(value.(string) + "\n")
			return nil
		}
		data, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		files[path] = data
		return nil
	}
	if err := add("server.yaml", snapshot.Server); err != nil {
		return nil, err
	}
	// A channel name is scoped to its parent, just as it is in the sidebar.
	identities := map[string]string{}
	for _, entry := range snapshot.Entries {
		if !safeName(entry.Name) || (entry.Parent != "" && !safeName(entry.Parent)) {
			return nil, fmt.Errorf("unsafe entry name %q", entry.Name)
		}
		if (entry.Category == nil) == (entry.Channel == nil) {
			return nil, fmt.Errorf("entry %q must be category or channel", entry.Name)
		}
		directory := filepath.Join("channels", entry.Parent, entry.Name)
		id := ""
		filename := "channel.yaml"
		var value any = entry.Channel
		if entry.Category != nil {
			if entry.Parent != "" {
				return nil, fmt.Errorf("nested category")
			}
			id = entry.Category.ID
			filename = "category.yaml"
			value = entry.Category
		} else {
			id = entry.Channel.ID
		}
		if previousID, exists := identities[directory]; exists {
			return nil, fmt.Errorf("duplicate export entry %q: IDs %q and %q", directory, previousID, id)
		}
		identities[directory] = id
		if entry.Unmanaged != "" {
			filename = "UNMANAGED.md"
			value = strings.SplitN(entry.Unmanaged, "\n", 2)[0]
		}
		if err := add(filepath.Join(directory, filename), value); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// writeExportFiles preflights every destination before changing any file. Each
// replacement is atomic; the set of files is not a directory-wide transaction.
func writeExportFiles(root string, files map[string][]byte, force bool) error {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		destination := filepath.Join(root, path)
		info, err := os.Lstat(destination)
		if err == nil {
			if !force {
				return fmt.Errorf("export destination exists: %s (use force)", path)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("not a regular file: %s", path)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, path := range paths {
		destination := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(destination), ".export-*")
		if err != nil {
			return err
		}
		temporaryPath := file.Name()
		_, err = file.Write(files[path])
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(temporaryPath, destination)
		}
		if err != nil {
			os.Remove(temporaryPath)
			return err
		}
	}
	return nil
}
