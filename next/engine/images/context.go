package images

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/box"
)

type Runner interface {
	Run(context.Context, []string, ...string) ([]byte, error)
}

// ErrImageChoice is a repository with several images and nothing to say which. A compose file chooses one with build.
var ErrImageChoice = errors.New("the repository has several images, so a compose file must choose one")

// imageBranch holds the images of a repository whose default branch must stay free of Dyna files,
// such as a fork that mirrors its upstream.
const imageBranch = "refs/heads/dyna-images"

// Context reads a toolchain image from the mirrored default branch or the image branch: the one called wanted, or the sole
// image when wanted is empty.
func Context(ctx context.Context, runner Runner, mirror, wanted string) (string, TreeID, []byte, []box.Need, error) {
	name, tree, err := chooseImage(ctx, runner, mirror, wanted)
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

// DefaultSize reads the size file of a toolchain image, chosen as in Context. It returns "" when there is none.
func DefaultSize(ctx context.Context, runner Runner, mirror, wanted string) (string, error) {
	name, tree, err := chooseImage(ctx, runner, mirror, wanted)
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

// dynaFolder finds a folder of .dyna on the default branch, or else on the image branch. It returns "" when neither has it.
func dynaFolder(ctx context.Context, runner Runner, mirror, folder string) (string, error) {
	root, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", "HEAD", folder)
	if err != nil {
		return "", err
	}
	if len(root) == 0 {
		commit, err := runner.Run(ctx, nil, "--git-dir", mirror, "for-each-ref", "--format=%(objectname)", imageBranch)
		if err != nil || len(commit) == 0 {
			return "", err
		}
		root, err = runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", strings.TrimSpace(string(commit)), folder)
		if err != nil || len(root) == 0 {
			return "", err
		}
	}
	fields := strings.Fields(string(root))
	if len(fields) != 4 || fields[1] != "tree" {
		return "", errors.New("invalid directory " + folder)
	}
	return fields[2], nil
}

type imageEntry struct {
	name string
	tree TreeID
}

func imageEntries(ctx context.Context, runner Runner, mirror string) ([]imageEntry, error) {
	root, err := dynaFolder(ctx, runner, mirror, ".dyna/images")
	if err != nil || root == "" {
		return nil, err
	}
	listed, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", root)
	if err != nil {
		return nil, err
	}
	var entries []imageEntry
	for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, errors.New("invalid image entry")
		}
		if fields[1] != "tree" {
			continue
		}
		if fields[3] == "" || strings.Trim(fields[3], "abcdefghijklmnopqrstuvwxyz0123456789_.-") != "" {
			return nil, errors.New("invalid image name")
		}
		entries = append(entries, imageEntry{fields[3], TreeID(fields[2])})
	}
	return entries, nil
}

func chooseImage(ctx context.Context, runner Runner, mirror, wanted string) (string, TreeID, error) {
	entries, err := imageEntries(ctx, runner, mirror)
	if err != nil {
		return "", "", err
	}
	if wanted != "" {
		for _, entry := range entries {
			if entry.name == wanted {
				return entry.name, entry.tree, nil
			}
		}
		return "", "", errors.New("the repository has no image " + wanted)
	}
	switch len(entries) {
	case 0:
		return "", "", nil
	case 1:
		return entries[0].name, entries[0].tree, nil
	}
	return "", "", ErrImageChoice
}

// Folders lists the repository's image folders.
func Folders(ctx context.Context, runner Runner, mirror string) ([]string, error) {
	entries, err := imageEntries(ctx, runner, mirror)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.name
	}
	return names, nil
}

// ComposeBlob is one file of .dyna/compose.
type ComposeBlob struct {
	Name, Object string
	Bytes        int64
}

// ComposeBlobs lists .dyna/compose by file, found as images are. It keeps only plain files, so a symbolic link is not one.
func ComposeBlobs(ctx context.Context, runner Runner, mirror string) ([]ComposeBlob, error) {
	root, err := dynaFolder(ctx, runner, mirror, ".dyna/compose")
	if err != nil || root == "" {
		return nil, err
	}
	listed, err := runner.Run(ctx, nil, "--git-dir", mirror, "ls-tree", "-l", root)
	if err != nil {
		return nil, err
	}
	var blobs []ComposeBlob
	for _, line := range strings.Split(strings.TrimSpace(string(listed)), "\n") {
		meta, file, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
			continue
		}
		name, ok := strings.CutSuffix(file, ".yaml")
		if !ok {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return nil, errors.New("invalid compose entry")
		}
		blobs = append(blobs, ComposeBlob{Name: name, Object: fields[2], Bytes: size})
	}
	return blobs, nil
}

// ReadBlob reads one object from the mirror.
func ReadBlob(ctx context.Context, runner Runner, mirror, object string) ([]byte, error) {
	return runner.Run(ctx, nil, "--git-dir", mirror, "cat-file", "blob", object)
}
