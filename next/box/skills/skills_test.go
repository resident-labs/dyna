package skills

import (
	"github.com/resident-labs/dyna/contract"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const skillFile = "---\nname: pdf-tools\ndescription: Read PDFs\n---\n\nBody.\n"

func TestParseRefusesWhatPiWouldNotLoad(t *testing.T) {
	meta, err := Parse([]byte("\xef\xbb\xbf---\r\nname: pdf-tools\r\ndescription: >\r\n  Read PDFs\r\n---\r\nBody\r\n"))
	if err != nil || meta.Name != "pdf-tools" || meta.Description != "Read PDFs" {
		t.Fatalf("%+v %v", meta, err)
	}
	for text, want := range map[string]string{
		"no frontmatter":                             "frontmatter",
		"---\nname: a\ndescription: b\n":             "frontmatter",
		"---\nname: a\n---\n":                        "description",
		"---\ndescription: b\n---\n":                 "skill name",
		"---\nname: Bad_Name\ndescription: b\n---\n": "skill name",
		"---\nname: a--b\ndescription: b\n---\n":     "skill name",
		"---\nname: [\n---\n":                        "frontmatter",
	} {
		if _, err := Parse([]byte(text)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", text, err)
		}
	}
}

func TestTheDefaultSkillIsBuiltIn(t *testing.T) {
	byName := map[string]contract.Addon{}
	for _, item := range BuiltIn() {
		byName[item.Name] = item
	}
	if extend := byName["dyna-extend"]; len(byName) != 10 || !strings.HasPrefix(extend.Description, "Use when sessions need a skill, Pi package or MCP server") || !extend.BuiltIn || byName["dyna-discord"].Name == "" {
		t.Fatalf("%+v", byName)
	}
	if !IsBuiltIn("dyna-extend") || IsBuiltIn("other") {
		t.Fatal("IsBuiltIn")
	}
	if images := byName["dyna-images"]; !strings.HasPrefix(images.Description, "Use when your box lacks a tool") {
		t.Fatalf("%+v", images)
	}
	nodes := byName["dyna-nodes"]
	if !strings.HasPrefix(nodes.Description, "Root only. Use when the room needs more or other machines") {
		t.Fatalf("%+v", nodes)
	}
	if data, err := builtIn.ReadFile("dyna-images/" + File); err != nil || strings.Count(string(data), "\n") > 150 {
		t.Fatalf("%v", err)
	} else {
		for _, want := range []string{".dyna/compose/NAME.yaml", "session compose_list", "the room has several images", "dyna image save NAME"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("dyna-images does not say %q", want)
			}
		}
	}
	dir := t.TempDir()
	if err := Install(dir, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dyna-extend", File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dyna skill save", "dyna package add", "dyna mcp add", "next box start", "unless the owner tells you otherwise", ".pi/skills", "--root-only", "dyna skill set NAME --host", "is a built-in skill", "dyna addon set"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	data, err = os.ReadFile(filepath.Join(dir, "dyna-nodes", File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"create NAME SIZE", "destroy NAME", "list ", "--per-session", "--max", "--idle", "room hosts", "--only", "Podman 5", "FUSE", "user_allow_other", "OpenSSH", "authorized_keys", "--host-data", "dormant included", "host add NAME --ssh", "--discard-boxes", "#!/usr/bin/env bash"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
}

func TestTheWorkflowSkillSaysWhatTheCodeDoes(t *testing.T) {
	data, err := builtIn.ReadFile("dyna-workflows/" + File)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Parse(data)
	if err != nil || meta.Name != "dyna-workflows" || !strings.HasPrefix(meta.Description, "Use when you write, run, resume or reload a Dyna workflow script") {
		t.Fatalf("%+v %v", meta, err)
	}
	text := string(data)
	for _, want := range []string{
		"`JSON.parse(args)`", "`{code, stdout, stderr, durationMs}`", "`check` defaults to `true`", "`{failed: true, step, reason}`",
		"`{step, text, branch, commit, files}`", "`{step, output, branch, commit, files}`", "`{branch, commit, merged, conflicts}`", "`r.output.issues`",
		"A run reports only to its parent", "`merge()` reads `commit`, not the tip of `branch`", "A resume is a new session", "through any chain of resumes",
		"the run ended first", "`Failed steps (N): <names>`", "`data.json`", "Up to 1024 bytes", "AGENTS.md changes reach a running worker", "one worker per 512 MiB", "holds a box slot",
		"dry run of one group", "Do not prohibit the tools a worker needs", "still reports done",
		"`cwd` is relative to the main tree unless absolute", "`maxWorkers`", "git worktree remove --force", "`dyna session stop SESSION`",
		"Reload is for a running run, resume for an ended one", "`dyna workflow reload RUN FILE`", "no second one starts", "bounded read-only work with a schema result", "small has 2 CPU, 4 GiB", "six child", "[Context update]", "dyna workflow steer RUN STEP TEXT", "dyna workflow pause RUN", "dyna workflow unpause RUN", "runs on until the run ends",
		"script version", "[events.md](events.md)",
		"the worker submits as a JSON file", "`status(text)`", "It is not a step", "options other than `lane`", "nothing is read from a step's name",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	if lines := strings.Count(text, "\n"); lines > 150 {
		t.Errorf("the skill is %d lines", lines)
	}
	for _, word := range []string{"you must", "always", "never"} {
		if strings.Contains(strings.ToLower(text), word) {
			t.Errorf("the skill says %q: it states facts", word)
		}
	}
}

func TestWorkflowEventsGuideIsInstalled(t *testing.T) {
	guide, err := builtIn.ReadFile("dyna-workflows/events.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(guide)
	for _, want := range []string{"```js\nexport const meta", "```sh\ndyna room repo add ROOM OWNER/REPO --pr-events on", "dyna subscription add", "args.event", "await state.get()", "await state.set({ head })", "kind=pushed,opened", "subscription fire", "subscription update", "subscription state"} {
		if !strings.Contains(text, want) {
			t.Errorf("events guide does not say %q", want)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if len(line) > 120 {
			t.Errorf("events guide line is %d characters: %s", len(line), line)
		}
	}
	for _, root := range []bool{true, false} {
		dir := t.TempDir()
		if err := Install(dir, root); err != nil {
			t.Fatal(err)
		}
		copied, err := os.ReadFile(filepath.Join(dir, "dyna-workflows", "events.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(copied) != text {
			t.Fatalf("events guide changed during install (root=%v)", root)
		}
	}
}

func TestAnArchiveCarriesAFolderAndStaysTheSame(t *testing.T) {
	dir := t.TempDir()
	for path, mode := range map[string]os.FileMode{File: 0o644, "scripts/run.sh": 0o755, ".git/config": 0o644} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(skillFile), mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	req, err := Request(dir, "", "room", false)
	if err != nil || req.Room != "room" {
		t.Fatalf("%+v %v", req.Room, err)
	}
	again, err := Request(dir, "", "room", false)
	if err != nil || string(again.Archive) != string(req.Archive) {
		t.Fatal("the same folder packed differently")
	}
	meta, err := Inspect(req.Archive)
	if err != nil || meta.Name != "pdf-tools" {
		t.Fatalf("%+v %v", meta, err)
	}
	out := t.TempDir()
	if err := Extract(req.Archive, out); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(out, "scripts", "run.sh"))
	if err != nil || info.Mode()&0o100 == 0 {
		t.Fatalf("%v %v", info, err)
	}
	for _, gone := range []string{".git", "link"} {
		if _, err := os.Lstat(filepath.Join(out, gone)); err == nil {
			t.Errorf("%s came along", gone)
		}
	}
}

func TestExtractStaysInItsFolder(t *testing.T) {
	archive := NewArchive()
	if err := archive.Add("../outside", 0o600, []byte("x")); err != nil {
		t.Fatal(err)
	}
	data, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := Extract(data, t.TempDir()); err == nil {
		t.Fatal("a name left the folder")
	}
}

func TestRequestFromALinkOrAFile(t *testing.T) {
	req, err := Request("https://example.test/SKILL.md", "", "", true)
	if err != nil || req.URL == "" || req.Archive != nil || !req.Global {
		t.Fatalf("%+v %v", req, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mine.md"), []byte(skillFile), 0o600); err != nil {
		t.Fatal(err)
	}
	req, err = Request("mine.md", dir, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if meta, err := Inspect(req.Archive); err != nil || meta.Name != "pdf-tools" {
		t.Fatalf("%+v %v", meta, err)
	}
	if _, err := Request(t.TempDir(), "", "", false); err == nil || !strings.Contains(err.Error(), "has no SKILL.md") {
		t.Fatal(err)
	}
}

func TestRequestTakesHostAndPath(t *testing.T) {
	for in, want := range map[string][2]string{"omarchy:~/.claude/skills/hypruse": {"omarchy", "~/.claude/skills/hypruse"}, "ari-pc:/srv/s": {"ari-pc", "/srv/s"}} {
		req, err := Request(in, "", "", true)
		if err != nil || string(req.Host) != want[0] || req.Path != want[1] || len(req.Archive) != 0 {
			t.Fatalf("%s: %+v %v", in, req, err)
		}
	}
	if req, err := Request("https://x.test/a:b", "", "", true); err != nil || req.URL == "" || req.Host != "" {
		t.Fatalf("%+v %v", req, err)
	}
}

func TestAFolderNamedLikeHostAndPathNeedsDotSlash(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "my:skill"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my:skill", File), []byte("---\nname: my-skill\ndescription: x\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Request("my:skill", dir, "", true); err == nil || !strings.Contains(err.Error(), "./my:skill") {
		t.Fatalf("%v", err)
	}
	if req, err := Request("./my:skill", dir, "", true); err != nil || len(req.Archive) == 0 || req.Host != "" {
		t.Fatalf("%+v %v", req, err)
	}
}

func TestAnnotateNamesEveryRunOnHost(t *testing.T) {
	for hosts, want := range map[string]string{"omarchy": "omarchy", "omarchy desktop": "omarchy or desktop", "omarchy desktop arch": "omarchy, desktop or arch"} {
		var names []contract.HostName
		for _, h := range strings.Fields(hosts) {
			names = append(names, contract.HostName(h))
		}
		out, err := Annotate([]byte("---\nname: x\ndescription: Does x\n---\nBody\n"), names)
		if err != nil || !strings.Contains(string(out), "Does x · runs on "+want+"\"") || !strings.Contains(string(out), "run on a host: "+want+", through bash with host.") || !strings.HasSuffix(string(out), "this box.\nBody\n") {
			t.Fatalf("%q %v", out, err)
		}
	}
}

func TestCISkill(t *testing.T) {
	if !IsBuiltIn("dyna-ci") {
		t.Fatal("CI skill is not discoverable")
	}
	data, err := builtIn.ReadFile("dyna-ci/" + File)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Parse(data)
	if err != nil || meta.Name != "dyna-ci" || meta.Description != "Use when you write, debug or speed up a GitHub Actions workflow, or its runs look stuck." {
		t.Fatalf("%+v %v", meta, err)
	}
	text := string(data)
	for _, want := range []string{"dyna-medium", "6 hours", "CARGO_INCREMENTAL=0", "RUSTC_WRAPPER", "DYNA_CI_POOL_UNTIL", "DYNA_CI_BASE", "public", "fork", "job step status", "Jobs on GitHub-hosted runners", "Swatinem/rust-cache", "[profile.ci]", "inherits = \"dev\"", "incremental = false", "debug = 0", "github.event.pull_request.head.repo.full_name", "cargo test --profile ci", "dyna-admin", "dyna-diagnose"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(text, "\n") > 120 {
		t.Fatal("skill exceeds 120 lines")
	}
}

func TestAdminSkill(t *testing.T) {
	data, err := builtIn.ReadFile("dyna-admin/" + File)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Parse(data)
	if err != nil || meta.Name != "dyna-admin" || !strings.HasPrefix(meta.Description, "Root only. Use when") || len(meta.Description) >= 300 {
		t.Fatalf("%+v %v", meta, err)
	}
	text := string(data)
	for _, want := range []string{"dyna-audience: root", "--github-writes GROUPS@REPOS", "`actions`", "room repo add ROOM", "room repo ci", "room hosts ROOM --only", "room limit ROOM --cpus N --mem SIZE", "workflow set --room ROOM", "session set-account SESSION", "account limit ACCOUNT", "addon set KIND NAME", "host set NAME", "profile set NAME", "op decide", "op list ROOM", "24 hours", "full owner\n  power", "**Yours**", "**Every room**", "**The owner's**"} {
		if !strings.Contains(text, want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	if lines := strings.Count(text, "\n"); lines > 150 {
		t.Errorf("the skill is %d lines", lines)
	}
}

func TestRootOnlySkills(t *testing.T) {
	root := map[string]bool{"dyna-admin": true, "dyna-diagnose": true, "dyna-local-proxy": true, "dyna-nodes": true, "dyna-services": true}
	total := 0
	for _, item := range BuiltIn() {
		data, err := builtIn.ReadFile(item.Name + "/" + File)
		if err != nil {
			t.Fatal(err)
		}
		marked := strings.Contains(string(data), "\ndyna-audience: root\n")
		if marked != root[item.Name] || strings.HasPrefix(item.Description, "Root only. ") != root[item.Name] {
			t.Errorf("%s: root only is %v, marked %v, description %q", item.Name, root[item.Name], marked, item.Description)
		}
		if len(item.Description) >= 300 || !strings.Contains(item.Description, "Use when") {
			t.Errorf("%s: description %q", item.Name, item.Description)
		}
		total += len(item.Description)
	}
	if total > 1700 {
		t.Errorf("the descriptions take %d characters", total)
	}
}

func TestTheDiagnoseSkillIsForRoot(t *testing.T) {
	data, err := builtIn.ReadFile("dyna-diagnose/" + File)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Parse(data)
	if err != nil || meta.Name != "dyna-diagnose" || !strings.HasPrefix(meta.Description, "Root only. Use when") || len(meta.Description) >= 300 {
		t.Fatalf("%+v %v", meta, err)
	}
	text := string(data)
	for _, want := range []string{"dyna-audience: root", "beforeSeq", "no_hello", "room-limit", "model_failed", "journalctl --user", "engine host named in your prompt", "When runs look stuck"} {
		if !strings.Contains(text, want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	if lines := strings.Count(text, "\n"); lines > 150 {
		t.Errorf("the skill is %d lines", lines)
	}
}

func TestTheServicesSkillIsForRoot(t *testing.T) {
	data, err := builtIn.ReadFile("dyna-services/" + File)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := Parse(data)
	if err != nil || meta.Name != "dyna-services" || !strings.HasPrefix(meta.Description, "Root only. ") || len(meta.Description) > 300 {
		t.Fatalf("%+v %v", meta, err)
	}
	text := string(data)
	for _, want := range []string{"dyna-audience: root", "dyna service set ROOM --file F", "replaces the whole declaration", "dyna service secret set ROOM NAME", "dyna service update ROOM NAME", "dyna service rollback ROOM NAME", "dyna service logs ROOM NAME", "on: [service.failed]", "Log lines are not masked", "deletes the data volume"} {
		if !strings.Contains(text, want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	if lines := strings.Count(text, "\n"); lines > 150 {
		t.Errorf("the skill is %d lines", lines)
	}
}

// The watcher the dyna-ci skill hands out is the repository's own, so a fix to one reaches the other.
func TestPoolWatchCopyMatchesTheScript(t *testing.T) {
	skill, err := os.ReadFile("dyna-ci/pool-watch.py")
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile("../../scripts/ci-pool-watch.py")
	if err != nil {
		t.Fatal(err)
	}
	if string(skill) != string(script) {
		t.Fatal("box/skills/dyna-ci/pool-watch.py differs from scripts/ci-pool-watch.py")
	}
}
