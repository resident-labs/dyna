package localci

// This file removes Dyna's own CI caches on a CI machine. A CI machine keeps
// the caches and the run directories of the CI runs; nothing else on that
// machine belongs to Dyna, so every path here comes from an allow-list.

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/shellquote"
)

// ciCacheCleanAge is how long a run directory or a cache entry stays. Runs
// remove their own directory when they finish, so this only clears leftovers
// from a run whose engine or SSH client died.
const ciCacheCleanAge = 48 * time.Hour

// ciDiskFloorGB is the free space below which a run cleans the caches before
// it starts.
// TODO: read machines.ciDiskFloorGB once the disk floor setting lands, and
// drop this constant then.
const ciDiskFloorGB = 20

// legacyCacheDir is where the old single-node runner kept its caches. Runs that
// name no node still use it, so its entries stay until their age passes.
const legacyCacheDir = "/var/tmp/dyna-local-ci"

// cacheBase names the one directory a node keeps on its machine. Setup and
// cleanup both use it, so a cleanup always visits the tree a run writes to.
// The user ID is the controller's, not the SSH account's.
func cacheBase(node string) string {
	return fmt.Sprintf("/var/tmp/dyna-local-ci-%d/%s", os.Getuid(), node)
}

//go:embed cache_clean.sh
var ciCacheCleanBody string

// CacheCleanResult is what one cleanup removed on one CI machine.
type CacheCleanResult struct {
	Node       string   `json:"node"`
	DryRun     bool     `json:"dryRun"`
	Paths      []string `json:"paths,omitempty"`
	FreedBytes int64    `json:"freedBytes"`
}

// cacheCleanCommand is the one shell command a run sends to a CI machine. The
// script checks the allow-list itself, so a bad root stops it there.
func cacheCleanCommand(node string, dryRun bool, age time.Duration) string {
	dry := "0"
	if dryRun {
		dry = "1"
	}
	return "DYNA_CI_CLEAN_DRY=" + dry +
		" DYNA_CI_CLEAN_AGE_MIN=" + strconv.FormatInt(int64(age/time.Minute), 10) +
		" DYNA_CI_CLEAN_ROOTS=" + shellquote.Quote(cacheBase(node)+" "+legacyCacheDir) +
		" bash -o pipefail -c " + shellquote.Quote(ciCacheCleanBody)
}

// CleanCaches removes Dyna's CI caches and finished run directories on node.
// A dry run reports what it would remove and deletes nothing.
func (r Runner) CleanCaches(ctx context.Context, node Node, dryRun bool) (CacheCleanResult, error) {
	result := CacheCleanResult{Node: node.Name, DryRun: dryRun}
	if err := node.Validate(); err != nil {
		return result, err
	}
	// One cleanup never holds a run open for longer than this, whatever the
	// machine looks like.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var out, stderr tailBuffer
	nodeRunner := r
	nodeRunner.Node = &node
	if err := nodeRunner.nodeCommand(ctx, &out, &stderr, "", cacheCleanCommand(node.Name, dryRun, ciCacheCleanAge)); err != nil {
		return result, fmt.Errorf("clean %s caches: %w: %s", node.Name, err, stderr.String())
	}
	result.Paths, result.FreedBytes = parseCacheClean(out.String())
	return result, nil
}

var cacheCleanItem = regexp.MustCompile(`(?m)^DYNA_CI_CACHE_CLEAN_ITEM ([0-9]+) (.+)$`)
var cacheCleanFreed = regexp.MustCompile(`(?m)^DYNA_CI_CACHE_CLEAN_FREED_KB=([0-9]+)$`)

// parseCacheClean reads the script's two kinds of report line.
func parseCacheClean(output string) ([]string, int64) {
	var paths []string
	var freed int64
	for _, match := range cacheCleanItem.FindAllStringSubmatch(output, -1) {
		kb, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		freed += kb * 1024
		paths = append(paths, match[2])
	}
	if match := cacheCleanFreed.FindStringSubmatch(output); match != nil {
		if kb, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			freed = kb * 1024
		}
	}
	return paths, freed
}

// runKeeper is the shell command that keeps a run's lock held on the node for
// as long as the run lasts, and no longer: the keeper goes away with its lock
// when the run directory is removed, and it gives up after the longest run
// anyway. A crashed run therefore leaves only the lock file, never a held lock.
// A caller starts it as its own background statement.
func runKeeper(active string) string {
	wait := "end=$((SECONDS+10800)); while [ -e " + shellquote.Quote(active) + " ] && [ $SECONDS -lt $end ]; do sleep 5; done"
	return "setsid flock -n " + shellquote.Quote(active) + " bash -c " + shellquote.Quote(wait) + " >/dev/null 2>&1 </dev/null"
}

// diskFullNote is the text the kernel and the tools print when a check runs
// out of space. It is not a test failure, so the run treats it as an
// infrastructure failure.
var diskFullNote = regexp.MustCompile(`(?i)no space left on device|\bENOSPC\b`)

// diskFull reports whether text says that the disk on the CI machine is full.
func diskFull(text string) bool { return diskFullNote.MatchString(text) }

// runOutOfDisk reports whether any check or the run itself ran out of disk.
func runOutOfDisk(result Result) bool {
	if diskFull(result.Error) {
		return true
	}
	for _, check := range result.Suites {
		if diskFull(check.Error) || diskFull(check.LogTail) {
			return true
		}
	}
	for _, check := range result.Checks {
		if diskFull(check.Error) || diskFull(check.LogTail) {
			return true
		}
	}
	return false
}

// freeSpaceBytes asks the node for the free space of the file system that holds
// its CI directories. It is one df call, never a measurement of other folders.
func (r Runner) freeSpaceBytes(ctx context.Context, node Node, path string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var out, stderr tailBuffer
	nodeRunner := r
	nodeRunner.Node = &node
	if err := nodeRunner.nodeCommand(ctx, &out, &stderr, "", "df -Pk "+shellquote.Quote(path)); err != nil {
		return 0, fmt.Errorf("read free space of %s: %w: %s", path, err, stderr.String())
	}
	return parseDFAvailable(out.String())
}

// parseDFAvailable reads the "Available" column of one df -Pk line.
func parseDFAvailable(output string) (int64, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) < 2 {
		return 0, errors.New("df printed no file system line")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, fmt.Errorf("df line is incomplete: %q", lines[len(lines)-1])
	}
	kb, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("df available space is not a number: %q", fields[3])
	}
	return kb * 1024, nil
}

// runNodeWithDiskRetry runs the checks on the node once and, when they stop on
// a full disk, cleans Dyna's own caches and runs them once more. A second
// disk-full failure stays an infrastructure failure, so nothing retries it.
func (r Runner) runNodeWithDiskRetry(ctx context.Context, req Request) (Result, error) {
	// Only the attempt that is kept saves its result, so a reader never sees
	// the full-disk result of a run that is about to be retried.
	save := !req.DeferSave
	req.DeferSave = true
	result, err := r.runNode(ctx, req)
	if node := r.Node; node != nil && ctx.Err() == nil && runOutOfDisk(result) {
		cleaned, cleanErr := r.CleanCaches(ctx, *node, false)
		if cleanErr != nil {
			log.Printf("local CI %s: cleanup before the retry: %v", node.Name, cleanErr)
		} else {
			log.Printf("local CI %s: the disk was full; cleanup freed %d bytes from %d paths, so the run retries once", node.Name, cleaned.FreedBytes, len(cleaned.Paths))
		}
		if ctx.Err() == nil {
			result, err = r.runNode(ctx, req)
		}
	}
	if !save || ctx.Err() != nil {
		return result, err
	}
	if e := (Store{DataDir: r.root()}).SaveResult(context.Background(), result); e != nil {
		err = errors.Join(err, e)
	}
	return result, err
}

// cleanBeforeRun frees Dyna's caches when the node is close to a full disk, and
// reports how much it freed in the run log.
func (r Runner) cleanBeforeRun(ctx context.Context, node Node) error {
	free, err := r.freeSpaceBytes(ctx, node, "/var/tmp")
	if err != nil {
		return err
	}
	floor := int64(ciDiskFloorGB) << 30
	if free >= floor {
		return nil
	}
	cleaned, err := r.CleanCaches(ctx, node, false)
	if err != nil {
		return err
	}
	log.Printf("local CI %s: %d MB free before the run, cleanup freed %d bytes from %d paths",
		node.Name, free>>20, cleaned.FreedBytes, len(cleaned.Paths))
	return nil
}
