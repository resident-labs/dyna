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
	if extend := byName["dyna-extend"]; extend.Description != "Use when you need to add a skill or mcp server to dyna" || !extend.BuiltIn || byName["dyna-discord"].Name == "" {
		t.Fatalf("%+v", byName)
	}
	if !IsBuiltIn("dyna-extend") || IsBuiltIn("other") {
		t.Fatal("IsBuiltIn")
	}
	if images := byName["dyna-images"]; !strings.HasPrefix(images.Description, "Use when your box lacks a tool") {
		t.Fatalf("%+v", images)
	}
	nodes := byName["dyna-nodes"]
	if nodes.Description != "Use when you need to add nodes to Dyna, or a room needs its own or disposable machines" {
		t.Fatalf("%+v", nodes)
	}
	dir := t.TempDir()
	if err := Install(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "dyna-extend", File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dyna skill save", "dyna package add", "dyna mcp add", "next box start", "unless the owner tells you otherwise", ".pi/skills"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the skill does not say %q", want)
		}
	}
	data, err = os.ReadFile(filepath.Join(dir, "dyna-nodes", File))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"create NAME SIZE", "destroy NAME", "list ", "--per-session", "--max", "--idle", "room hosts", "--only", "Podman 5", "FUSE", "user_allow_other", "OpenSSH", "SSH key", "/home/pip/.local/share/dyna-next-host", "dormant session keeps its node", "#!/usr/bin/env bash"} {
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
	if err != nil || meta.Name != "dyna-workflows" || !strings.HasPrefix(meta.Description, "Use when you write, test or resume a Dyna workflow, or reload a running one") {
		t.Fatalf("%+v %v", meta, err)
	}
	text := string(data)
	for _, want := range []string{
		"`JSON.parse(args)`", "`{code, stdout, stderr, durationMs}`", "`check` defaults to `true`", "`{failed: true, step, reason}`",
		"`{step, text, branch, commit, files}`", "`{step, output, branch, commit, files}`", "`{branch, commit, merged, conflicts}`", "`r.output.issues`",
		"A crew reports only to its parent", "`merge()` reads `commit`, not the tip of `branch`", "A resume is a new session", "through any chain of resumes",
		"the run ended first", "needs no polling", "Nothing wakes a worker", "only through the prompt", "one worker per 512 MiB", "holds a box slot",
		"dry run of one group", "give up in 40 seconds", "still reports done",
		"Reload is for a running run, resume for an ended one", "`dyna workflow reload RUN FILE`", "no second one starts", "runs on until the run ends",
		"`meta.name`, `meta.repo`, `meta.size` and `meta.gpu` stay", "the run's models follow the new profiles", "the version that started it",
		"the worker submits a JSON file with its `submit` tool", "`status(text)`", "It is not a step", "`75 of 76 steps · 1 running`", "`{lane: g.id}`", "options other than `lane`", "Nothing is read from a step's name",
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
	if err != nil || meta.Name != "dyna-ci" || meta.Description != "Use when you write, debug or speed up a GitHub Actions workflow, whether its jobs run on Dyna's self-hosted runners or on GitHub-hosted ones" {
		t.Fatalf("%+v %v", meta, err)
	}
	text := string(data)
	for _, want := range []string{"dyna-medium", "6 hours", "CARGO_INCREMENTAL=0", "RUSTC_WRAPPER", "ci_scale", "DYNA_CI_BASE", "public", "fork", "job step status", "Jobs on GitHub-hosted runners", "Swatinem/rust-cache", "[profile.ci]", "inherits = \"dev\"", "incremental = false", "debug = 0", "github.event.pull_request.head.repo.fork", "cargo test --profile ci", "half the CPUs of the runners already there"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(text, "\n") > 90 {
		t.Fatal("skill exceeds 90 lines")
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
