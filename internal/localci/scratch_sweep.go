package localci

// Dyna's scratch folders on a CI machine grow without a session to close them:
// a task can name its folder itself, so the engine host's closed-session
// retention cannot apply there. This sweep judges a folder by its last file
// change, never by its name, and a free-space guard removes idle build outputs
// first when the machine's disk blocks new work.

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/scratch"
	"dyna-agent/internal/shellquote"
)

// scratchBuildIdle is how long a build output must sit untouched before the
// free-space guard may remove it.
const scratchBuildIdle = 6 * time.Hour

//go:embed scratch_sweep.sh
var scratchSweepBody string

// ScratchFolder is one folder of a machine's scratch root. Newest is the last
// change of any file inside it. Builds are its build outputs (target,
// node_modules, build, dist, .gradle), as paths inside the folder.
type ScratchFolder struct {
	Name   string
	Newest time.Time
	Bytes  int64
	Builds []ScratchBuild
}

// ScratchBuild is one build output inside a scratch folder.
type ScratchBuild struct {
	Path   string
	Newest time.Time
	Bytes  int64
}

// ScratchHost is one machine's scratch root. Remove takes a path relative to
// the root and removes nothing outside it.
type ScratchHost interface {
	List(ctx context.Context) ([]ScratchFolder, error)
	Free(ctx context.Context) (int64, error)
	Remove(ctx context.Context, rel string) error
}

// ScratchResult is what one sweep freed on a machine.
type ScratchResult struct {
	Node string
	// Folders and Builds count the folders removed by retention and the build
	// outputs removed by the free-space guard.
	Folders, Builds int
	FreedBytes      int64
	// FreeBytes is the free space after the sweep, when the guard measured it.
	FreeBytes int64
}

// ScratchReporter tells the rooms that use a machine what a sweep freed.
type ScratchReporter func(ctx context.Context, node Node, result ScratchResult) error

// SweepScratch removes every folder that no file has changed in for
// scratch.ClosedRetention. Then, when free space is under floor, it removes
// build outputs idle for scratchBuildIdle or more, oldest first, until free
// space reaches floor. Callers pass the boundary of the node's disk rule, so a
// sweep ends with a node that admits work again. A zero floor turns the guard
// off, such as for a node that was never probed.
func SweepScratch(ctx context.Context, host ScratchHost, now time.Time, floor int64) (ScratchResult, error) {
	var result ScratchResult
	folders, err := host.List(ctx)
	if err != nil {
		return result, err
	}
	var errs []error
	var kept []ScratchFolder
	for _, folder := range folders {
		if now.Sub(folder.Newest) <= scratch.ClosedRetention {
			kept = append(kept, folder)
			continue
		}
		if err := host.Remove(ctx, folder.Name); err != nil {
			errs = append(errs, err)
			kept = append(kept, folder)
			continue
		}
		result.Folders++
		result.FreedBytes += folder.Bytes
	}
	if floor <= 0 {
		return result, errors.Join(errs...)
	}
	free, err := host.Free(ctx)
	if err != nil {
		return result, errors.Join(append(errs, err)...)
	}
	type build struct {
		rel string
		ScratchBuild
	}
	var builds []build
	for _, folder := range kept {
		for _, b := range folder.Builds {
			if now.Sub(b.Newest) >= scratchBuildIdle {
				builds = append(builds, build{path.Join(folder.Name, b.Path), b})
			}
		}
	}
	sort.SliceStable(builds, func(i, j int) bool { return builds[i].Newest.Before(builds[j].Newest) })
	for _, b := range builds {
		if free >= floor || ctx.Err() != nil {
			break
		}
		if err := host.Remove(ctx, b.rel); err != nil {
			errs = append(errs, err)
			continue
		}
		result.Builds++
		result.FreedBytes += b.Bytes
		if free, err = host.Free(ctx); err != nil {
			errs = append(errs, err)
			break
		}
	}
	result.FreeBytes = free
	return result, errors.Join(errs...)
}

// nodeScratch runs the scratch script on a CI machine over its SSH path.
type nodeScratch struct {
	r    Runner
	node Node
}

func (h nodeScratch) run(ctx context.Context, env string) (string, error) {
	var out, stderr tailBuffer
	runner := h.r
	runner.Node = &h.node
	command := "DYNA_SCRATCH_MODE=" + env + " bash -o pipefail -c " + shellquote.Quote(scratchSweepBody)
	if err := runner.nodeCommand(ctx, &out, &stderr, command); err != nil {
		return "", fmt.Errorf("scratch on %s: %w: %s", h.node.Name, err, stderr.String())
	}
	return out.String(), nil
}

func (h nodeScratch) List(ctx context.Context) ([]ScratchFolder, error) {
	out, err := h.run(ctx, "list")
	if err != nil {
		return nil, err
	}
	return parseScratchList(out), nil
}

func (h nodeScratch) Free(ctx context.Context) (int64, error) {
	out, err := h.run(ctx, "free")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(out, "\n") {
		if kb, ok := strings.CutPrefix(line, "FREE\t"); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(kb), 10, 64)
			return n * 1024, err
		}
	}
	return 0, errors.New("scratch script printed no free space")
}

func (h nodeScratch) Remove(ctx context.Context, rel string) error {
	_, err := h.run(ctx, "remove DYNA_SCRATCH_PATH="+shellquote.Quote(rel))
	return err
}

// parseScratchList reads the script's FOLDER and BUILD lines. A line it cannot
// read is skipped, so a folder it cannot judge is never removed.
func parseScratchList(out string) []ScratchFolder {
	var folders []ScratchFolder
	index := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 4 || (f[0] != "FOLDER" && f[0] != "BUILD") {
			continue
		}
		epoch, err1 := strconv.ParseInt(f[1], 10, 64)
		kb, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		newest, bytes := time.Unix(epoch, 0), kb*1024
		switch {
		case f[0] == "FOLDER":
			index[f[3]] = len(folders)
			folders = append(folders, ScratchFolder{Name: f[3], Newest: newest, Bytes: bytes})
		case len(f) == 5:
			if i, ok := index[f[3]]; ok {
				folders[i].Builds = append(folders[i].Builds, ScratchBuild{Path: f[4], Newest: newest, Bytes: bytes})
			}
		}
	}
	return folders
}

// sweepNodeScratch sweeps the scratch root of a machine over SSH. The local
// machine is left to the engine's own closed-session retention.
func sweepNodeScratch(ctx context.Context, r Runner, node Node, now time.Time) (ScratchResult, error) {
	if node.SSH == "" {
		return ScratchResult{Node: node.Name}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	floor := int64(machines.KeepFreeBytes(node.MaxDiskPercent, node.Disk.Total))
	result, err := SweepScratch(ctx, nodeScratch{r: r, node: node}, now, floor)
	result.Node = node.Name
	return result, err
}
