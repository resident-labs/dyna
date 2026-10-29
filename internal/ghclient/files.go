package ghclient

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/approvals"
)

type fileArgument struct {
	index  int
	path   string
	prefix string
}

// SnapshotFiles reads every file-backed argument into the immutable payload.
// Standard input must be snapshotted by the shim, which owns that stream.
func SnapshotFiles(args []string, cwd string) ([]approvals.FileSnapshot, error) {
	refs, err := approvalFileArguments(commandArgs(args))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []approvals.FileSnapshot
	for _, ref := range refs {
		if seen[ref.path] {
			continue
		}
		seen[ref.path] = true
		if ref.path == "-" {
			return nil, errors.New("standard input needs an explicit approval snapshot")
		}
		path := ref.path
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files = append(files, approvals.FileSnapshot{Path: ref.path, Data: data})
	}
	return files, nil
}

// ValidateFileSnapshots rejects any readable file argument without one exact
// immutable snapshot. Conflicting duplicate snapshots are also ambiguous.
func ValidateFileSnapshots(args []string, files []approvals.FileSnapshot) error {
	replacements := map[string]string{}
	for _, file := range files {
		replacements[file.Path] = file.Path
	}
	seen := map[string][]byte{}
	for _, file := range files {
		if prior, ok := seen[file.Path]; ok && !bytes.Equal(prior, file.Data) {
			return fmt.Errorf("conflicting snapshots for %q", file.Path)
		}
		seen[file.Path] = file.Data
	}
	_, err := RewriteFileArgs(args, replacements)
	return err
}

// RewriteFileArgs points every file-backed argument at its restored snapshot.
func RewriteFileArgs(args []string, replacements map[string]string) ([]string, error) {
	argv := append([]string(nil), args...)
	command := commandArgs(argv)
	refs, err := approvalFileArguments(command)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		path, ok := replacements[ref.path]
		if !ok {
			return nil, fmt.Errorf("file argument %q has no immutable snapshot", ref.path)
		}
		command[ref.index] = ref.prefix + path
	}
	return argv, nil
}

func approvalFileArguments(args []string) ([]fileArgument, error) {
	if len(args) == 0 {
		return nil, nil
	}
	if args[0] == "api" {
		return apiFileArguments(args)
	}
	if len(args) < 2 {
		return nil, nil
	}
	var refs []fileArgument
	for i := 2; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			flag, value, attached := strings.Cut(arg, "=")
			takes := flag == "--repo" || repositoryFlagTakesValue(args[:2], flag)
			if !attached && takes && i+1 < len(args) {
				i++
				value = args[i]
			}
			if flag == "--body-file" {
				prefix := ""
				if attached {
					prefix = strings.TrimSuffix(arg, value)
				}
				refs = append(refs, fileArgument{index: i, path: value, prefix: prefix})
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		for j := 1; j < len(arg); j++ {
			flag := "-" + arg[j:j+1]
			if arg[j] != 'R' && !repositoryFlagTakesValue(args[:2], flag) {
				continue
			}
			value, valueIndex, prefix := strings.TrimPrefix(arg[j+1:], "="), i, strings.TrimSuffix(arg, strings.TrimPrefix(arg[j+1:], "="))
			if j+1 == len(arg) && i+1 < len(args) {
				i++
				value, valueIndex, prefix = args[i], i, ""
			}
			if flag == "-F" {
				refs = append(refs, fileArgument{index: valueIndex, path: value, prefix: prefix})
			}
			break
		}
	}
	return validateFileArguments(refs)
}

func apiFileArguments(args []string) ([]fileArgument, error) {
	var refs []fileArgument
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			flag, value, attached := strings.Cut(arg, "=")
			takes := flag == "--repo" || flag == "--hostname" || repositoryFlagTakesValue(args[:1], flag)
			if !attached && takes && i+1 < len(args) {
				i++
				value = args[i]
			}
			prefix := ""
			if attached {
				prefix = strings.TrimSuffix(arg, value)
			}
			switch flag {
			case "--input":
				refs = append(refs, fileArgument{index: i, path: value, prefix: prefix})
			case "--field":
				if ref, ok := apiFieldFile(i, value, prefix); ok {
					refs = append(refs, ref)
				}
			}
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		for j := 1; j < len(arg); j++ {
			flag := "-" + arg[j:j+1]
			if arg[j] != 'R' && !repositoryFlagTakesValue(args[:1], flag) {
				continue
			}
			value, valueIndex, prefix := strings.TrimPrefix(arg[j+1:], "="), i, strings.TrimSuffix(arg, strings.TrimPrefix(arg[j+1:], "="))
			if j+1 == len(arg) && i+1 < len(args) {
				i++
				value, valueIndex, prefix = args[i], i, ""
			}
			if flag == "-F" {
				if ref, ok := apiFieldFile(valueIndex, value, prefix); ok {
					refs = append(refs, ref)
				}
			}
			break
		}
	}
	return validateFileArguments(refs)
}

func apiFieldFile(index int, value, prefix string) (fileArgument, bool) {
	_, raw, ok := strings.Cut(value, "=")
	if !ok || !strings.HasPrefix(raw, "@") {
		return fileArgument{}, false
	}
	path := strings.TrimPrefix(raw, "@")
	return fileArgument{index: index, path: path, prefix: strings.TrimSuffix(prefix+value, path)}, true
}

func validateFileArguments(refs []fileArgument) ([]fileArgument, error) {
	for _, ref := range refs {
		if ref.path == "" {
			return nil, errors.New("file-backed approval argument has an empty path")
		}
	}
	return refs, nil
}
