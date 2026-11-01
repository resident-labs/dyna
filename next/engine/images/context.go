package images

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/box"
)

type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

var ErrImageChoice = errors.New("choosing an image is not supported yet")

// imageBranch holds the images of a repository whose default branch must stay free of Dyna files,
// such as a fork that mirrors its upstream.
const imageBranch = "refs/heads/dyna-images"

// Context reads the sole toolchain image from the mirrored default branch or the image branch.
func Context(ctx context.Context, runner Runner, mirror string) (string, TreeID, []byte, []box.Need, error) {
	name, tree, err := soleImage(ctx, runner, mirror)
	if err != nil || name == "" {
		return "", "", nil, nil, err
	}
	entry, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", string(tree), "box-needs")
	if err != nil {
		return "", "", nil, nil, err
	}
	var needs []box.Need
	if len(entry) > 0 {
		data, err := runner.Run(ctx, nil, "--git-dir", mirror, "show", string(tree)+":box-needs")
		if err != nil {
			return "", "", nil, nil, err
		}
		for _, word := range strings.Fields(string(data)) {
			need := box.Need(word)
			switch need {
			case box.NeedNestedContainers, box.NeedKVM:
				if !slices.Contains(needs, need) {
					needs = append(needs, need)
				}
			default:
				return "", "", nil, nil, errors.New("unknown box need: " + word)
			}
		}
	}
	data, err := runner.Run(ctx, nil, "--git-dir", mirror, "archive", "--format=tar", string(tree))
	return name, tree, data, needs, err
}

// DefaultSize reads the size file of the sole toolchain image. It returns "" when there is none.
func DefaultSize(ctx context.Context, runner Runner, mirror string) (string, error) {
	name, tree, err := soleImage(ctx, runner, mirror)
	if errors.Is(err, ErrImageChoice) {
		return "", nil // several images: no repository default
	}
	if err != nil || name == "" {
		return "", err
	}
	out, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", string(tree), "size")
	if err != nil || len(out) == 0 {
		return "", err
	}
	data, err := runner.Run(ctx, nil, "--git-dir", mirror, "show", string(tree)+":size")
	return strings.TrimSpace(string(data)), err
}

func soleImage(ctx context.Context, runner Runner, mirror string) (string, TreeID, error) {
	root, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", "HEAD", ".dyna/images")
	if err != nil {
		return "", "", err
	}
	if len(root) == 0 {
		commit, err := runner.Run(ctx, nil, "--git-dir", mirror, "for-each-ref", "--format=%(objectname)", imageBranch)
		if err != nil || len(commit) == 0 {
			return "", "", err
		}
		root, err = runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", strings.TrimSpace(string(commit)), ".dyna/images")
		if err != nil || len(root) == 0 {
			return "", "", err
		}
	}
	fields := strings.Fields(string(root))
	if len(fields) != 4 || fields[1] != "tree" {
		return "", "", errors.New("invalid image directory")
	}
	entries, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", fields[2])
	if err != nil {
		return "", "", err
	}
	var name string
	var tree TreeID
	for _, line := range strings.Split(strings.TrimSpace(string(entries)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return "", "", errors.New("invalid image entry")
		}
		if fields[1] != "tree" {
			continue
		}
		if name != "" {
			return "", "", ErrImageChoice
		}
		name, tree = fields[3], TreeID(fields[2])
		if name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_.-") != "" {
			return "", "", errors.New("invalid image name")
		}
	}
	return name, tree, nil
}
