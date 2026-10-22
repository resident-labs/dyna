package localci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/shellquote"
)

type nodeCheck struct {
	id, name, inner, image string
}

// WorkflowSuites includes the conditional jobs needed for a complete trial.
// DefaultSuites retains the manual SSH lane's historical suite selection.
func WorkflowSuites() []string {
	return []string{"relay", "go", "cross-build", "computer-smoke", "android"}
}

func nodeChecks(suites []string) ([]nodeCheck, bool, error) {
	if len(suites) == 0 {
		suites = WorkflowSuites()
	}
	var checks []nodeCheck
	seen := map[string]bool{}
	add := func(id, name, inner, image string) {
		if !seen[id] {
			checks = append(checks, nodeCheck{id, name, inner, image})
			seen[id] = true
		}
	}
	aggregate := false
	for _, suite := range suites {
		switch suite {
		case "relay":
			add(suite, suite, "bash scripts/ci.sh relay", "node:22")
		case "frontend":
			add(suite, suite, "bash scripts/ci.sh frontend test", "node:22")
		case "frontend-check":
			add(suite, suite, "bash scripts/ci.sh frontend check", "node:22")
		case "go", "go-test":
			aggregate = true
			add("frontend", "frontend", "bash scripts/ci.sh frontend test", "node:22")
			add("frontend-check", "frontend-check", "bash scripts/ci.sh frontend check", "node:22")
			add("go-static", "go-static", "bash scripts/ci.sh go modules && bash scripts/ci.sh go vet && bash scripts/ci.sh go installer", "golang:1.26.3")
			for i := 0; i < 8; i++ {
				inner := fmt.Sprintf("export CI_SHARD=%d/8 CI_SLICE=''; ", i)
				if i == 0 {
					inner += "export CI_SLICE=0/2; "
				} else if i == 7 {
					inner += "export CI_SLICE=1/2; "
				}
				inner += "bash scripts/ci.sh go build && bash scripts/ci.sh go test"
				if i == 4 {
					inner += " && touch web/dist/index.html && bash scripts/ci.sh go desktop"
				}
				add(fmt.Sprintf("go-test-shard-%d", i), fmt.Sprintf("go-test-shard (%d)", i), inner, "golang:1.26.3")
			}
		case "go-static":
			add(suite, suite, "bash scripts/ci.sh go modules && bash scripts/ci.sh go vet && bash scripts/ci.sh go installer", "golang:1.26.3")
		case "cross-build":
			add(suite, suite, "bash scripts/ci.sh cross-build", "golang:1.26.3")
		case "android":
			add(suite, suite, "command -v curl >/dev/null && command -v unzip >/dev/null || exit 69; bash scripts/ci.sh android", "eclipse-temurin:17-jdk")
		case "computer-smoke":
			add(suite, suite, "", "")
		default:
			return nil, false, fmt.Errorf("unknown CI suite %q", suite)
		}
	}
	return checks, aggregate, nil
}

func (r Runner) nodeCommand(ctx context.Context, out, stderr io.Writer, scope, script string) error {
	fn := r.Command
	if fn == nil {
		fn = command
	}
	name, args := "bash", []string{"-o", "pipefail", "-c", script}
	if scope != "" {
		name = "systemd-run"
		args = append([]string{"--user", "--scope", "--quiet", "--expand-environment=no", "--unit", scope, "-p", "CPUQuota=200%", "-p", "MemoryMax=5G", "-p", "TasksMax=768", "--", "nice", "-n", "10", "bash", "-o", "pipefail", "-c"}, script)
	}
	if r.Node.SSH != "" {
		remote := shellquote.Quote(name)
		for _, arg := range args {
			remote += " " + shellquote.Quote(arg)
		}
		name, args = "ssh", []string{"-o", "BatchMode=yes"}
		if r.Node.Jump != "" {
			args = append(args, "-J", r.Node.Jump)
		}
		args = append(args, r.Node.SSH, remote)
	}
	return fn(ctx, "", nil, out, stderr, name, args...)
}

func (r Runner) runNode(ctx context.Context, req Request) (result Result, err error) {
	checks, aggregate, err := nodeChecks(req.Suites)
	if err != nil {
		return result, err
	}
	node := *r.Node
	if err := node.Validate(); err != nil {
		return result, err
	}
	if !node.Enabled {
		return result, fmt.Errorf("CI node is disabled")
	}
	if req.Timeout <= 0 {
		req.Timeout = 30 * time.Minute
	}
	if req.MaxTest <= 0 {
		req.MaxTest = 10 * time.Second
	}
	repo, err := filepath.Abs(req.Repo)
	if err != nil {
		return result, err
	}
	common, err := r.commonDir(ctx, repo)
	if err != nil {
		return result, err
	}
	ref := req.SHA
	if ref == "" {
		ref = "HEAD"
	}
	var output bytes.Buffer
	if err := r.run(ctx, repo, &output, "git", "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		return result, fmt.Errorf("resolve commit: %w: %s", err, &output)
	}
	sha := strings.TrimSpace(output.String())
	if !regexp.MustCompile(`^[0-9a-f]{40,64}$`).MatchString(sha) {
		return result, fmt.Errorf("git returned invalid commit SHA")
	}
	result = Result{Repo: common, RoomID: req.RoomID, Node: node.Name, SHA: sha, Started: time.Now().UTC(), Status: "error"}
	defer func() {
		result.Duration = time.Since(result.Started)
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
			return
		}
		if err != nil {
			result.Status, result.Error = "error", err.Error()
		}
		if !req.DeferSave {
			err = errors.Join(err, (Store{DataDir: r.root()}).SaveResult(context.Background(), result))
		}
	}()
	base := fmt.Sprintf("/var/tmp/dyna-local-ci-%d/%s", os.Getuid(), node.Name)
	cache := path.Join(base, repoKey(common)+".git")
	runID := fmt.Sprintf("%s-%d", sha[:12], time.Now().UnixNano())
	runDir := path.Join(base, "runs", runID)
	reports := path.Join(runDir, "reports")
	dir := r.resultDir(common, sha)
	if err := os.MkdirAll(dir, 0700); err != nil { // not-state: report blobs live outside SQLite.
		return result, err
	}
	reportRoot, err := os.MkdirTemp(dir, "run-") // not-state: report blobs live outside SQLite.
	if err != nil {
		return result, err
	}
	control, cancel := context.WithTimeout(ctx, time.Duration(len(checks))*(req.Timeout+admissionWait)+2*time.Minute)
	defer cancel()
	output.Reset()
	setup := "set -e; umask 077; mkdir -p " + shellquote.Quote(base) + " " + shellquote.Quote(reports) + "; flock " + shellquote.Quote(path.Join(base, "init.lock")) + " bash -c " + shellquote.Quote("if [ ! -d "+shellquote.Quote(cache)+" ]; then git init --bare "+shellquote.Quote(cache)+"; fi")
	if err := r.nodeCommand(control, &output, &output, "", setup); err != nil {
		return result, fmt.Errorf("initialize node: %w: %s", err, &output)
	}
	output.Reset()
	if node.SSH == "" {
		err = r.run(control, repo, &output, "git", "push", cache, sha+":refs/dyna/ci/"+sha)
	} else {
		args := []string{}
		if node.Jump != "" {
			args = append(args, "-c", "core.sshCommand=ssh -o BatchMode=yes -J "+shellquote.Quote(node.Jump))
		}
		args = append(args, "push", node.SSH+":"+cache, sha+":refs/dyna/ci/"+sha)
		err = r.run(control, repo, &output, "git", args...)
	}
	if err != nil {
		return result, fmt.Errorf("push commit to node: %w: %s", err, &output)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		var log tailBuffer
		// Stop daemon-owned containers before scopes, so a scope keeps its slot
		// until its container has stopped, including after a lost SSH client.
		script := "set -e; docker=(docker); "
		if node.SSH == "" {
			script += "if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); fi; "
		}
		for _, check := range checks {
			name := "dyna-ci-" + runID + "-" + check.id
			if check.image != "" {
				script += `timeout 20s "${docker[@]}" rm -f ` + shellquote.Quote(name) + " >/dev/null 2>&1 || true; "
			}
			if check.image != "" {
				script += `if [ "$(timeout 10s "${docker[@]}" inspect -f '{{.State.Running}}' ` + shellquote.Quote(name) + ` 2>/dev/null)" = true ]; then echo 'container cleanup failed' >&2; exit 69; fi; `
			}
			script += "timeout 10s systemctl --user stop " + shellquote.Quote(name+".scope") + " >/dev/null 2>&1 || true; "
			script += "git --git-dir=" + shellquote.Quote(cache) + " worktree remove --force " + shellquote.Quote(path.Join(runDir, check.id, "work")) + " >/dev/null 2>&1 || true; "
		}
		script += "rm -rf " + shellquote.Quote(runDir) + "; git --git-dir=" + shellquote.Quote(cache) + " worktree prune"
		if e := r.nodeCommand(cleanup, &log, &log, "", script); e != nil {
			err = errors.Join(err, fmt.Errorf("cleanup node: %w: %s", e, log.String()))
		}
	}()
	if req.OnStart != nil {
		if err := req.OnStart(control); err != nil {
			return result, err
		}
	}
	result.Suites = make([]SuiteResult, len(checks))
	defer func() {
		result.Checks = append([]SuiteResult(nil), result.Suites...)
		if aggregate {
			status := "passed"
			for _, check := range result.Suites {
				if check.Suite == "frontend" || check.Suite == "frontend-check" || check.Suite == "go-static" || strings.HasPrefix(check.Suite, "go-test-shard-") {
					status = combinedStatus(status, check.Status)
				}
			}
			result.Checks = append(result.Checks, SuiteResult{Suite: "go-test", CheckName: "go-test", Node: node.Name, Status: status})
		}
	}()
	jobs := make(chan int, len(checks))
	for i := range checks {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	for worker := 0; worker < min(node.Slots, len(checks)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				check := checks[i]
				sr := SuiteResult{Suite: check.id, CheckName: check.name, Node: node.Name, Status: "passed"}
				checkCtx, stop := context.WithTimeout(control, admissionWait+req.Timeout+time.Minute)
				var log tailBuffer
				e := r.nodeCommand(checkCtx, &log, &log, "dyna-ci-"+runID+"-"+check.id, nodeCheckScript(node, base, cache, runDir, runID, sha, check, req.Timeout, i))
				sr.LogTail = log.String()
				markers := regexp.MustCompile(`(?m)^DYNA_CI_CHECK_EXIT=([0-9]+)$`).FindAllStringSubmatch(sr.LogTail, -1)
				if e != nil || len(markers) == 0 {
					sr.Status, sr.Error = "error", "check command did not complete"
					if e != nil {
						sr.Error = e.Error()
					}
					if len(markers) != 0 && checkCtx.Err() == nil {
						code, _ := strconv.Atoi(markers[len(markers)-1][1])
						switch code {
						case 0, 69, 75, 124, 125, 126, 127, 137, 143, 255:
						default:
							sr.Status = "failed"
						}
					}
				}
				stop()
				result.Suites[i] = sr
			}
		}()
	}
	wg.Wait()
	if control.Err() != nil {
		return result, control.Err()
	}
	archive, err := os.CreateTemp(reportRoot, "reports-*.tar") // not-state: temporary report transport.
	if err != nil {
		return result, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	script := "cd " + shellquote.Quote(reports) + " && find . -mindepth 2 -maxdepth 2 -type f \\( -name '*.xml' -o -name '*.json' -o -name 'ci-diagnostics.jsonl' \\) -print0 | tar -C " + shellquote.Quote(reports) + " --null -T - -cf -"
	var stderr tailBuffer
	if err := r.nodeCommand(control, &boundedWriter{writer: archive, remaining: 70 << 20}, &stderr, "", script); err != nil {
		return result, fmt.Errorf("copy reports: %w: %s", err, stderr.String())
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	if err := readArchive(archive, reportRoot); err != nil {
		return result, err
	}
	result.Status = "passed"
	for i := range result.Suites {
		sr := &result.Suites[i]
		var timing struct {
			DurationNanos time.Duration `json:"durationNanos"`
		}
		timingPath := filepath.Join(reportRoot, "reports", sr.Suite, "execution.json")
		data, timingErr := os.ReadFile(timingPath)
		if timingErr == nil {
			timingErr = json.Unmarshal(data, &timing)
		}
		if timingErr == nil && timing.DurationNanos >= 0 {
			sr.Duration = timing.DurationNanos
		} else if sr.Status == "passed" {
			sr.Status, sr.Error = "error", "check produced no valid execution timing"
		}
		if e := checkReports(sr, reportRoot, req.MaxTest, req.Allowed); e != nil {
			if sr.Status != "failed" {
				sr.Status = "error"
			}
			sr.Error = e.Error()
		}
		diagnostics := filepath.Join(reportRoot, "reports", sr.Suite, "ci-diagnostics.jsonl")
		if info, e := os.Stat(diagnostics); e == nil && info.Mode().IsRegular() {
			sr.Reports = append(sr.Reports, diagnostics)
		}
		result.Status = combinedStatus(result.Status, sr.Status)
	}
	return result, nil
}

func combinedStatus(a, b string) string {
	if a == "error" || b == "error" {
		return "error"
	}
	if a == "failed" || b == "failed" {
		return "failed"
	}
	return "passed"
}

// Slots belong to checks, across repositories and runner processes. A waiter
// tries every slot before blocking on one slot without polling.
func nodeSlotScript(base string, slots, index int, inner string) string {
	return "set -euo pipefail; mkdir -p " + shellquote.Quote(path.Join(base, "slots")) + "; acquired=0; " +
		fmt.Sprintf("for slot in $(seq 0 %d); do exec 9>\"%s/slots/$slot.lock\"; if flock -n 9; then acquired=1; break; fi; done; ", slots-1, base) +
		fmt.Sprintf("if [ \"$acquired\" = 0 ]; then exec 9>%s; flock -w %d 9 || exit 75; fi; ", shellquote.Quote(path.Join(base, "slots", fmt.Sprintf("%d.lock", index%slots))), int(admissionWait.Seconds())) + inner
}

func nodeCheckScript(node Node, base, cache, runDir, runID, sha string, check nodeCheck, timeout time.Duration, index int) string {
	work := path.Join(runDir, check.id, "work")
	reportDir := path.Join(runDir, "reports", check.id)
	name := "dyna-ci-" + runID + "-" + check.id
	q := shellquote.Quote
	seconds := int64((timeout + time.Second - 1) / time.Second)
	docker := "docker=(docker); "
	if node.SSH == "" {
		docker += "if ! docker info >/dev/null 2>&1; then docker=(sudo -n docker); if ! \"${docker[@]}\" info >/dev/null 2>&1; then echo 'Docker access is unavailable' >&2; exit 69; fi; fi; "
	}
	timing := `printf '{"durationNanos":%s}\n' "$(( $(date +%s%N) - started ))" > ` + q(path.Join(reportDir, "execution.json")) + "; "
	cleanup := "check_status=$?; " + timing + `if [ "$check_entered" = 1 ]; then printf '\nDYNA_CI_CHECK_EXIT=%s\n' "$check_status"; fi; "${docker[@]}" rm -f ` + q(name) + " >/dev/null 2>&1 || true; git --git-dir=" + q(cache) + " worktree remove --force " + q(work) + " >/dev/null 2>&1 || true"
	setup := "mkdir -p " + q(reportDir) + "; started=$(date +%s%N); check_entered=0; " + docker + "trap " + q(cleanup) + " EXIT; trap 'exit 143' HUP INT TERM; mkdir -p " + q(path.Dir(work)) + "; git --git-dir=" + q(cache) + " worktree add --detach " + q(work) + " " + q(sha) + " || exit 69; "
	if check.id == "computer-smoke" {
		setup += "for tool in go labwc Xwayland sqlite3 agent-browser; do command -v \"$tool\" >/dev/null || { echo \"Missing computer-smoke prerequisite: $tool\" >&2; exit 69; }; done; "
		setup += "cd " + q(work) + "; mkdir -p " + q(path.Join(base, "go-build")) + " " + q(path.Join(base, "go-mod")) + "; isolated=$(mktemp -d); trap " + q(cleanup+`; rm -rf "$isolated"`) + " EXIT; "
		setup += fmt.Sprintf("check_entered=1; timeout --signal=TERM --kill-after=10s %ds env -i PATH=\"$PATH\" HOME=\"$isolated/home\" XDG_DATA_HOME=\"$isolated/data\" XDG_CONFIG_HOME=\"$isolated/config\" XDG_CACHE_HOME=\"$isolated/cache\" XDG_RUNTIME_DIR=\"${XDG_RUNTIME_DIR:-/run/user/$(id -u)}\" DBUS_SESSION_BUS_ADDRESS=\"${DBUS_SESSION_BUS_ADDRESS:-unix:path=/run/user/$(id -u)/bus}\" GOCACHE=%s GOMODCACHE=%s GOMAXPROCS=2 GOFLAGS=-p=2 DYNA_COMPUTERS_ENABLED=0 DYNA_COMPUTERS_REQUIRE_LIVE=1 go test -count=1 -timeout 5m ./internal/computers", seconds, q(path.Join(base, "go-build")), q(path.Join(base, "go-mod")))
		return nodeSlotScript(base, node.Slots, index, setup)
	}
	for _, cacheDir := range []string{"go-build", "go-mod", "npm", "android", "gradle"} {
		setup += "mkdir -p " + q(path.Join(base, cacheDir)) + "; "
	}
	inner := "mkdir -p /tmp/home /tmp/data /tmp/config /tmp/cache; " + check.inner
	user := " --user $(id -u):$(id -g)"
	if strings.HasPrefix(check.image, "golang:") || check.id == "android" {
		user = ""
		prepare := ""
		if check.id == "go-static" {
			prepare = "timeout 120s apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=15 update -qq && timeout 120s apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=15 install -y -qq shellcheck >/dev/null || exit 69; "
		}
		if check.id == "android" {
			prepare = "if ! command -v curl >/dev/null || ! command -v unzip >/dev/null; then timeout --signal=TERM --kill-after=10s 180s bash -c " + q("apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=15 -o Acquire::https::Timeout=15 update -qq && apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=15 -o Acquire::https::Timeout=15 install -y -qq curl unzip >/dev/null") + " || exit 69; fi; "
		}
		inner = prepare + "(getent group $CI_GID >/dev/null || groupadd --gid $CI_GID dyna-ci) && (getent passwd $CI_UID >/dev/null || useradd --no-log-init --uid $CI_UID --gid $CI_GID --no-create-home --home-dir /tmp/home dyna-ci) && exec setpriv --reuid=$CI_UID --regid=$CI_GID --clear-groups bash -c " + q(inner)
	}
	args := `"${docker[@]}" run --rm --init --cpus=2 --memory=4g --memory-swap=4g --pids-limit=512 --name ` + q(name) + user +
		" -v " + q(work+":"+work) + " -v " + q(cache+":"+cache) + " -v " + q(reportDir+":/reports") + " -w " + q(work)
	for _, mount := range [][2]string{{"go-build", "build"}, {"go-mod", "mod"}, {"npm", "npm"}, {"android", "android"}, {"gradle", "gradle"}} {
		args += " -v " + q(path.Join(base, mount[0])+":/cache/"+mount[1])
	}
	args += " -e HOME=/tmp/home -e XDG_DATA_HOME=/tmp/data -e XDG_CONFIG_HOME=/tmp/config -e XDG_CACHE_HOME=/tmp/cache -e RUNNER_TEMP=/reports -e GITHUB_STEP_SUMMARY=/reports/step-summary.md -e GOCACHE=/cache/build -e GOMODCACHE=/cache/mod -e npm_config_cache=/cache/npm -e ANDROID_HOME=/cache/android -e GRADLE_USER_HOME=/cache/gradle -e GOMAXPROCS=2 -e GOMEMLIMIT=3GiB -e GOFLAGS=-p=2 -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=" + q(work) + " -e CI_UID=$(id -u) -e CI_GID=$(id -g) -e JUNIT_REPORT_PATH=/reports/" + check.id + "-test.xml " + check.image
	// Both host and container deadlines survive a disconnected SSH client.
	setup += fmt.Sprintf("check_entered=1; timeout --signal=TERM --kill-after=10s %ds ", seconds+15) + args + fmt.Sprintf(" timeout --signal=TERM --kill-after=10s %ds bash -c ", seconds) + q(inner)
	return nodeSlotScript(base, node.Slots, index, setup)
}
