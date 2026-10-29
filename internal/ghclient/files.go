package ghclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"dyna-agent/internal/approvals"
)

type fileArgument struct {
	index  int
	name   string
	value  string
	path   string
	prefix string
	file   bool
}

// DisplayApprovalBody renders inline text and immutable snapshots through the
// same argument parser that binds execution. API fields keep their field names.
func DisplayApprovalBody(payload approvals.GHPayload) string {
	command := commandArgs(payload.Argv)
	refs, err := approvalFileArguments(command)
	if err != nil {
		return ""
	}
	snapshots := map[string]string{}
	for _, file := range payload.Files {
		snapshots[file.Path] = string(file.Data)
	}
	api := len(command) > 0 && command[0] == "api"
	var parts []string
	var body string
	for _, ref := range refs {
		content := ref.value
		if ref.file {
			content = snapshots[ref.path]
		}
		if !api && ref.name == "body" {
			body = content
			continue
		}
		if ref.name == "" {
			parts = append(parts, content)
		} else {
			parts = append(parts, ref.name+":\n"+content)
		}
	}
	if !api {
		return body
	}
	return strings.Join(parts, "\n\n")
}

// SnapshotFiles reads every file-backed argument into the immutable payload.
// Standard input must be snapshotted by the shim, which owns that stream.
func SnapshotFiles(args []string, cwd string, stdin io.Reader) ([]approvals.FileSnapshot, error) {
	refs, err := approvalFileArguments(commandArgs(args))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var files []approvals.FileSnapshot
	for _, ref := range refs {
		if !ref.file || seen[ref.path] {
			continue
		}
		seen[ref.path] = true
		if ref.path == "-" {
			if stdin == nil {
				return nil, errors.New("standard input needs an explicit approval snapshot")
			}
			data, err := io.ReadAll(stdin)
			if err != nil {
				return nil, err
			}
			files = append(files, approvals.FileSnapshot{Path: ref.path, Data: data})
			continue
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
	seen := map[string][]byte{}
	for _, file := range files {
		replacements[file.Path] = file.Path
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
		if !ref.file {
			continue
		}
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
			prefix := ""
			if attached {
				prefix = strings.TrimSuffix(arg, value)
			}
			switch flag {
			case "--body":
				refs = append(refs, fileArgument{index: i, name: "body", value: value})
			case "--body-file":
				refs = append(refs, fileArgument{index: i, name: "body", path: value, prefix: prefix, file: true})
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
			switch flag {
			case "-b":
				refs = append(refs, fileArgument{index: valueIndex, name: "body", value: value})
			case "-F":
				refs = append(refs, fileArgument{index: valueIndex, name: "body", path: value, prefix: prefix, file: true})
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
				refs = append(refs, fileArgument{index: i, path: value, prefix: prefix, file: true})
			case "--field":
				if ref, ok := apiFieldArgument(i, value, prefix, true); ok {
					refs = append(refs, ref)
				}
			case "--raw-field":
				if ref, ok := apiFieldArgument(i, value, prefix, false); ok {
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
			if flag == "-F" || flag == "-f" {
				if ref, ok := apiFieldArgument(valueIndex, value, prefix, flag == "-F"); ok {
					refs = append(refs, ref)
				}
			}
			break
		}
	}
	return validateFileArguments(refs)
}

func apiFieldArgument(index int, value, prefix string, allowFile bool) (fileArgument, bool) {
	name, raw, ok := strings.Cut(value, "=")
	if !ok {
		return fileArgument{}, false
	}
	ref := fileArgument{index: index, name: name, value: raw}
	if allowFile && strings.HasPrefix(raw, "@") {
		ref.path = strings.TrimPrefix(raw, "@")
		ref.prefix = strings.TrimSuffix(prefix+value, ref.path)
		ref.file = true
	}
	return ref, true
}

func validateFileArguments(refs []fileArgument) ([]fileArgument, error) {
	for _, ref := range refs {
		if ref.file && ref.path == "" {
			return nil, errors.New("file-backed approval argument has an empty path")
		}
	}
	return refs, nil
}
