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
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"dyna-agent/internal/settings"
	"dyna-agent/internal/shellquote"
)

// Runs remove their own directory when they finish, so the sweep only clears
// leftovers from a run whose engine or SSH client died. Cache entries stay
// longer because they make later runs faster, and cache_clean.sh also keeps
// each cache under a size cap so a busy machine cannot fill its disk.
const (
	ciRunCleanAge   = 2 * time.Hour
	ciCacheCleanAge = 48 * time.Hour
)

// ciDiskFloorBytes is the free space a CI machine must keep, as the owner's
// settings say it. The same floor holds new CI work and lets a run clean Dyna's
// caches first, so an owner changes one policy. A settings file that cannot be
// read keeps the default floor, so a broken file never leaves a full machine
// unclean. Zero turns cache recovery off, as it turns CI admission off.
func ciDiskFloorBytes() int64 {
	values, err := settings.Load("")
	if err != nil {
		return int64(settings.Defaults().CIDiskFloorBytes())
	}
	return int64(values.CIDiskFloorBytes())
}

// legacyCacheDir is where the old single-node runner kept its caches. No run
// writes there now, so its entries stay until their age passes.
const legacyCacheDir = "/var/tmp/dyna-local-ci"

// cacheBase names the one directory a node keeps on its machine. Setup and
// cleanup both use it, so a cleanup always visits the tree a run writes to.
// The user ID is the controller's, not the SSH account's.
func cacheBase(node string) string {
	return fmt.Sprintf("/var/tmp/dyna-local-ci-%d/%s", os.Getuid(), node)
}

func repositoryCacheRoot(base string) string { return path.Join(base, "caches") }

func repositoryCachePath(base, name string) string {
	return path.Join(repositoryCacheRoot(base), name)
}

//go:embed cache_clean.sh
var ciCacheCleanBody string

// baseLinkGuard refuses a managed base or its parent that is a symbolic link.
// A repair under a linked base would write outside Dyna's own tree, so the run
// stops instead, before it creates anything.
func baseLinkGuard(base string) string {
	q := shellquote.Quote
	guard := ""
	for _, unsafe := range []struct{ path, label string }{
		{path.Dir(base), "CI cache base parent"},
		{base, "CI cache base"},
	} {
		guard += "if [ -L " + q(unsafe.path) + " ]; then echo " +
			q("unsafe "+unsafe.label+" "+unsafe.path+" is a symlink to") + " \"$(readlink -- " + q(unsafe.path) + ")\" >&2; exit 69; fi; "
	}
	return guard
}

// cacheLinkGuard refuses a managed cache path that is still a symbolic link
// after the prepare stage. Docker and the node resolve a bind source link on
// the host, so such a link would make a run read, write and prune a cache
// outside Dyna's own tree. It also refuses a linked base or parent.
func cacheLinkGuard(base string, names []string) string {
	q := shellquote.Quote
	guard := baseLinkGuard(base)
	root := repositoryCacheRoot(base)
	guard += "if [ -L " + q(root) + " ]; then echo " +
		q("unsafe CI cache root "+root+" is a symlink to") + " \"$(readlink -- " + q(root) + ")\" >&2; exit 69; fi; "
	for _, name := range names {
		unsafe := repositoryCachePath(base, name)
		guard += "if [ -L " + q(unsafe) + " ]; then echo " +
			q("unsafe CI cache path "+unsafe+" is a symlink to") + " \"$(readlink -- " + q(unsafe) + ")\" >&2; exit 69; fi; "
	}
	return guard
}

// cacheRepairScript switches a linked go-build or go-mod path back to a real
// empty directory while it holds the cache lock exclusively, before the run
// takes its ordinary shared lock. It keeps the link itself beside the cache as
// <cache>.before-d48 and never changes or deletes its target. A link that
// resolves inside the managed base, a dangling link and an existing backup are
// unsafe, so they stop the run instead. A run with no linked path takes no
// exclusive lock and prints nothing.
func cacheRepairScript(base string, wait time.Duration) string {
	q := shellquote.Quote
	build, mod := repositoryCachePath(base, "go-build"), repositoryCachePath(base, "go-mod")
	repair := "b=" + q(base) + "; "
	for _, cachePath := range []string{build, mod} {
		backup := cachePath + ".before-d48"
		kind := path.Base(cachePath)
		repair += "if [ -L " + q(cachePath) + " ]; then " +
			"target=$(readlink -e -- " + q(cachePath) + " 2>/dev/null) || target=; " +
			"if [ -z \"$target\" ]; then echo " + q("refusing dangling CI cache link "+cachePath) + " >&2; exit 69; fi; " +
			"case \"$target\" in \"$b\"|\"$b\"/*) echo " +
			q("refusing unsafe CI cache link "+cachePath+" that resolves inside the managed base") + " >&2; exit 69 ;; esac; " +
			"if [ -e " + q(backup) + " ] || [ -L " + q(backup) + " ]; then echo " +
			q("refusing to overwrite CI cache backup "+backup) + " >&2; exit 69; fi; " +
			"mv -- " + q(cachePath) + " " + q(backup) + " || exit 69; " +
			"if ! mkdir -m 0755 -- " + q(cachePath) + "; then mv -- " + q(backup) + " " + q(cachePath) + "; exit 69; fi; " +
			"printf 'DYNA_CI_CACHE_REPAIR %s\\n' " + q(kind) + "; " +
			"fi; "
	}
	lock := "exec 7>>" + q(path.Join(base, "caches.lock")) + "; flock -w " + strconv.Itoa(int(wait.Seconds())) + " 7 || exit 75; "
	return "if [ -L " + q(build) + " ] || [ -L " + q(mod) + " ]; then " + lock + repair + "exec 7>&-; fi; "
}

// CacheKind names one managed cache path that a run can repair. The values are
// the path names under the repository cache root.
type CacheKind string

const (
	// CacheGoBuild is the Go build cache.
	CacheGoBuild CacheKind = "go-build"
	// CacheGoMod is the Go module cache, which also holds download and VCS
	// checkouts.
	CacheGoMod CacheKind = "go-mod"
)

// CacheRepair says which linked cache paths one run switched back to real
// directories on its machine.
type CacheRepair struct {
	Node  string
	Kinds []CacheKind
}

// CacheRepairReporter receives a completed cache repair. The runner calls it
// even when the run fails afterwards, so a repair is never silent.
type CacheRepairReporter func(context.Context, CacheRepair)

// cacheRepairLine is the transport fact the repair stage prints while it
// holds the exclusive cache lock. It is not the owner event; the runner turns
// the facts into a registered typed event.
var cacheRepairLine = regexp.MustCompile(`(?m)^DYNA_CI_CACHE_REPAIR (go-build|go-mod)$`)

// parseCacheRepairs reads the repair facts from the setup output in the order
// they happened, with no duplicate kind.
func parseCacheRepairs(output string) []CacheKind {
	var kinds []CacheKind
	for _, match := range cacheRepairLine.FindAllStringSubmatch(output, -1) {
		kind := CacheKind(match[1])
		if !slices.Contains(kinds, kind) {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

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
		" DYNA_CI_CLEAN_RUN_AGE_MIN=" + strconv.FormatInt(int64(ciRunCleanAge/time.Minute), 10) +
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
	if err := nodeRunner.nodeCommand(ctx, &out, &stderr, cacheCleanCommand(node.Name, dryRun, ciCacheCleanAge)); err != nil {
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

// runLocks is the shell command that takes a run's own lock and its base's
// shared cache lock, and leaves both held by a keeper. It returns once the
// keeper holds them, so a teardown always finds the process to stop. The setup
// holds the shared cache lock before it returns, so a cleanup that is trimming
// caches is never overtaken by a run that starts while it works, and a run that
// already runs makes that cleanup skip every entry. A crashed run leaves only
// the lock files behind, never a held lock: the keeper ends with the run
// directory and gives up after the longest run anyway.
func runLocks(base, active string) string {
	keeper := shellquote.Quote(keeperPID(active))
	wait := runKeeper(active)
	ready := "i=0; while [ ! -s " + keeper + " ] && [ $i -lt 200 ]; do sleep 0.01; i=$((i+1)); done; "
	return "exec 6>>" + shellquote.Quote(active) + "; flock -n -w 60 6; " +
		"exec 7>>" + shellquote.Quote(path.Join(base, "caches.lock")) + "; flock -s -w 60 7; " +
		"setsid bash -c " + shellquote.Quote(wait) + " 6>&6 7>&7 >/dev/null 2>&1 </dev/null & exec 6>&-; exec 7>&-; " + ready
}

// runKeeper holds a run's locks until its active file goes away. Its EXIT trap
// also removes the run directory: teardown stops the keeper before it removes
// the directory, so an interrupted SSH command cannot strand it between steps.
func runKeeper(active string) string {
	keeper := shellquote.Quote(keeperPID(active))
	remove := "rm -rf --one-file-system " + shellquote.Quote(path.Dir(active))
	return "trap " + shellquote.Quote(remove) + " EXIT; " +
		"echo $$ > " + keeper + "; end=$((SECONDS+10800)); " +
		"while [ -e " + shellquote.Quote(active) + " ] && [ $SECONDS -lt $end ]; do sleep 5; done"
}

// keeperPID is the file that names the keeper of one run, so its teardown can
// stop it instead of waiting for it to notice the run directory has gone.
func keeperPID(active string) string { return path.Join(path.Dir(active), ".keeper") }

// stopKeeper is the shell command that stops a run's keeper and returns only
// when the run's own lock is free, which means the keeper is gone and the
// base's shared cache lock was released with it. A cleanup that follows a
// finished run therefore frees the run's caches at once, and a retry after a
// full disk is not skipped by the run's own keeper. The wait is bounded, and
// another run's cache lock never delays this one.
func stopKeeper(base, active string) string {
	keeper := shellquote.Quote(keeperPID(active))
	// The keeper leads its own process group, and its sleeping child inherits the
	// lock file descriptors, so the group goes together: killing only the keeper
	// would leave the lock held until that sleep ends. The run's own lock is the
	// acknowledgement: only this run's keeper takes it, so once it is free the
	// keeper is gone and the base's shared cache lock is released with it. A
	// cleanup that waits for the base lock itself would wait for other runs.
	killIt := "k=$(cat " + keeper + "); kill -TERM -- -\"$k\" 2>/dev/null || kill \"$k\" 2>/dev/null || true"
	lock := shellquote.Quote(active)
	// Open the run lock before signalling the keeper. The keeper's EXIT trap
	// removes its pathname, so only this descriptor still names the inode whose
	// lock acknowledges that the keeper and its cache lock are gone.
	return "opened=0; if [ -e " + lock + " ]; then exec 9>>" + lock + " && opened=1; fi; " +
		"if [ -s " + keeper + " ]; then " + killIt + "; fi; " +
		"if [ \"$opened\" = 1 ]; then i=0; while [ $i -lt 50 ] && ! flock -n 9; do sleep 0.1; i=$((i+1)); done; exec 9>&-; fi; "
}

// removeRunDir is the command that ends a run on its machine: it stops the
// keeper, so the run's locks are free before it returns, and removes the run's
// own directory.
func removeRunDir(base, active string) string {
	return stopKeeper(base, active) + "rm -rf --one-file-system " + shellquote.Quote(path.Dir(active))
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
	if err := nodeRunner.nodeCommand(ctx, &out, &stderr, "df -Pk "+shellquote.Quote(path)); err != nil {
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
	// A run that reached its own deadline keeps its evidence, so the owner can
	// see the cause. A run its caller cancelled belongs to that caller, which
	// alone decides whether to save it: a deliberate cancel means the work will
	// run again, and saving it here would report it as a failure.
	if !save || cancelled(ctx) {
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
	floor := ciDiskFloorBytes()
	if floor <= 0 || free >= floor {
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
