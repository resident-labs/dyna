package localci

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/machines"
)

func TestRepositoryDefinitionDrivesNonGoRun(t *testing.T) {
	definition := parseRepositoryDefinition(t, false)
	if got := definition.SuitesForFiles([]string{"tests/test_service.py"}); len(got) != 1 || got[0] != "test" {
		t.Fatalf("Python test file selected %v", got)
	}
	checks, _, err := nodeChecks(definition, []Suite{"test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{DataDir: t.TempDir(), Node: &Node{Name: "local", Slots: 1, Enabled: true}}
	legacy := func(_ context.Context, dir string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		if fakeSystemdVersion(out, name, args) {
			return nil
		}
		if fakeNodeGit(dir, out, name, args) {
			return nil
		}
		script := args[len(args)-1]
		switch {
		case strings.Contains(script, "tar -C"):
			return fakeNodeReports(out, checks, false)
		case name == "systemd-run":
			for _, want := range []string{"python:3.13-slim", "python -m pytest", "/caches/pip:/cache/pip", "PIP_CACHE_DIR=/cache/pip"} {
				if !strings.Contains(script, want) {
					t.Errorf("configured check script lacks %q", want)
				}
			}
			fmt.Fprintln(out, "DYNA_CI_CHECK_EXIT=0")
		}
		return nil
	}
	newFakeMachine(t, legacy).attach(&runner)
	result, err := runner.Run(t.Context(), Request{Definition: definition, Repo: t.TempDir(), Suites: []Suite{"test"}, MaxTest: time.Second, DeferSave: true})
	if err != nil || result.Status != OutcomePassed {
		t.Fatalf("repository-defined run = %+v, %v", result, err)
	}
	if len(result.Checks) != 1 || result.Checks[0].Name() != "pytest" || result.Checks[0].Status != OutcomePassed {
		t.Fatalf("repository-defined checks = %+v", result.Checks)
	}
}

func TestHostCheckNeedsMachineConsent(t *testing.T) {
	definition := parseRepositoryDefinition(t, true)
	node := &Node{Name: "local", Slots: 1, Enabled: true}
	runner := Runner{DataDir: t.TempDir(), Node: node, Command: func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		t.Fatal("host check ran a command without machine consent")
		return nil
	}}
	_, err := runner.Run(t.Context(), Request{Definition: definition, Repo: t.TempDir(), Suites: []Suite{"test"}})
	if !errors.Is(err, ErrHostChecksDisabled) {
		t.Fatalf("host check error = %v, want ErrHostChecksDisabled", err)
	}

	checks, _, err := nodeChecks(definition, []Suite{"test"}, true)
	if err != nil {
		t.Fatal(err)
	}
	script := nodeCheckScript(*node, "/var/tmp/dyna-ci/local", "/var/tmp/dyna-ci/repo.git", "/var/tmp/dyna-ci/run", "run", strings.Repeat("a", 40), strings.Repeat("b", 40), checks[0], time.Minute, 0, false, DefaultActImage, machines.DefaultContainerSocket, "")
	for _, want := range []string{"env -i", "PIP_CACHE_DIR=/var/tmp/dyna-ci/local/caches/pip", "python -m pytest"} {
		if !strings.Contains(script, want) {
			t.Errorf("allowed host script lacks %q", want)
		}
	}
	if strings.Contains(script, "docker run") {
		t.Fatal("host check was placed in a container")
	}
}

// A room root's grant of host checks covers its own room only, so another
// room's repository commands stay in their container until someone allows them.
func TestHostChecksRunOnlyForTheGrantedRoom(t *testing.T) {
	definition := parseRepositoryDefinition(t, true)
	node := &Node{Name: "local", Slots: 1, Enabled: true,
		HostChecksGrants: []machines.HostChecksGrant{{By: machines.GrantorRoot, Room: "room_a"}}}
	if !node.HostChecksAllowed("room_a") {
		t.Fatal("the granting room was refused")
	}
	if node.HostChecksAllowed("room_b") {
		t.Fatal("one room's grant allowed another room's host checks")
	}
	runner := Runner{DataDir: t.TempDir(), Node: node, Command: func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
		t.Fatal("host check ran a command for a room that no one allows")
		return nil
	}}
	_, err := runner.Run(t.Context(), Request{Definition: definition, Repo: t.TempDir(), Suites: []Suite{"test"}, RoomID: "room_b"})
	if !errors.Is(err, ErrHostChecksDisabled) {
		t.Fatalf("host check for another room = %v, want ErrHostChecksDisabled", err)
	}
	// The owner's grant covers every room, including work that names none.
	owner := &Node{Name: "local", Slots: 1, Enabled: true,
		HostChecksGrants: []machines.HostChecksGrant{{By: machines.GrantorOwner}}}
	if !owner.HostChecksAllowed("room_b") || !owner.HostChecksAllowed("") {
		t.Fatal("the owner's grant did not cover every room")
	}
}

func parseRepositoryDefinition(t *testing.T, host bool) *Definition {
	t.Helper()
	execution := "image: python:3.13-slim"
	if host {
		execution = "host: true"
	}
	definition, err := ParseDefinition([]byte(`version: 1
trial: [test]
caches:
  - name: pip
    target: /cache/pip
    env: PIP_CACHE_DIR
checks:
  - id: pytest
    ` + execution + `
    command: python -m pytest --junitxml=/reports/pytest.xml
    caches: [pip]
    reports: junit
suites:
  - id: test
    files: ['^src/', '^tests/', '^pyproject\.toml$']
    checks: [pytest]
`))
	if err != nil {
		t.Fatal(err)
	}
	return definition
}

// This repository's own definition parses, so a broken definition is caught
// here instead of by the next pull request. The desktop module is a nested
// module the root Go commands never reach, so the go suite must still run it.
func TestThisRepositoryDefinitionParses(t *testing.T) {
	body, err := os.ReadFile("../../.dyna/local-ci.yaml")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := ParseDefinition(body)
	if err != nil {
		t.Fatalf("this repository's definition does not parse: %v", err)
	}
	commands, ok := definition.ExecutableChecks("go")
	if !ok {
		t.Fatal("this repository's definition has no go suite")
	}
	// The hosted workflow reports the desktop module as a job named desktop,
	// and the base graph requires that name: a local result without a check of
	// that name is rejected, so a trial could never finish. The module is
	// tested once, by that check, and not again inside a shard.
	var desktop int
	for _, command := range commands {
		if string(command.Check.Name) == "desktop" {
			desktop++
		}
		if string(command.Check.Name) != "desktop" && strings.Contains(command.Command, "scripts/ci.sh go desktop") {
			t.Fatalf("check %s runs the desktop module a second time", string(command.Check.Name))
		}
	}
	if desktop != 1 {
		t.Fatalf("the go suite has %d checks named desktop, want one", desktop)
	}
}
