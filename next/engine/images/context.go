package images

import (
	"context"
	"errors"
	"strings"
)

type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

var ErrImageChoice = errors.New("choosing an image is not supported yet")

// Context reads the sole toolchain image from the mirrored default branch.
func Context(ctx context.Context, runner Runner, mirror string) (string, TreeID, []byte, error) {
	root, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", "HEAD", ".dyna/images")
	if err != nil {
		return "", "", nil, err
	}
	if len(root) == 0 {
		return "", "", nil, nil
	}
	fields := strings.Fields(string(root))
	if len(fields) != 4 || fields[1] != "tree" {
		return "", "", nil, errors.New("invalid image directory")
	}
	entries, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", fields[2])
	if err != nil {
		return "", "", nil, err
	}
	var name string
	var tree TreeID
	for _, line := range strings.Split(strings.TrimSpace(string(entries)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return "", "", nil, errors.New("invalid image entry")
		}
		if fields[1] != "tree" {
			continue
		}
		if name != "" {
			return "", "", nil, ErrImageChoice
		}
		name, tree = fields[3], TreeID(fields[2])
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_.-") != "" {
			return "", "", nil, errors.New("invalid image name")
		}
	}
	if name == "" {
		return "", "", nil, nil
	}
	data, err := runner.Run(ctx, nil, "--git-dir", mirror, "archive", "--format=tar", string(tree))
	return name, tree, data, err
}
