package ci

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func routing(t *testing.T, env ...string) string {
	t.Helper()
	cmd := exec.Command("bash", "../../../scripts/ci-runner.sh")
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRoutingDefaultsToGitHubHosted(t *testing.T) {
	live := strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)
	expired := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	for name, env := range map[string][]string{
		"nothing set":     nil,
		"merged, not on":  {"SAME_REPO=true"},
		"switch off":      {"ENABLED=false", "SAME_REPO=true", "LEASE=" + live},
		"fork":            {"ENABLED=true", "SAME_REPO=false", "LEASE=" + live},
		"no pool yet":     {"ENABLED=true", "SAME_REPO=true"},
		"pool died":       {"ENABLED=true", "SAME_REPO=true", "LEASE=" + expired},
		"malformed lease": {"ENABLED=true", "SAME_REPO=true", "LEASE=soon"},
	} {
		if got := routing(t, env...); got != "self_hosted=false" {
			t.Errorf("%s: %s", name, got)
		}
	}
	if got := routing(t, "ENABLED=true", "SAME_REPO=true", "LEASE="+live); got != "self_hosted=true" {
		t.Fatalf("live pool: %s", got)
	}
}

// A workflow that takes a fork's code with write access, or a job that reaches
// the pool without the routing decision, would bypass the gate.
func TestWorkflowKeepsTheGate(t *testing.T) {
	data, err := os.ReadFile("../../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	if strings.Contains(workflow, "pull_request_target") {
		t.Fatal("ci.yml must not use pull_request_target")
	}
	for _, want := range []string{
		"ENABLED: ${{ vars.DYNA_CI_SELF_HOSTED }}",
		"LEASE: ${{ vars." + LeaseVariable + " }}",
		"SAME_REPO: ${{ github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository }}",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("ci.yml lost %q", want)
		}
	}
	routed := `runs-on: ${{ needs.changes.outputs.self_hosted == 'true' && '` + Name + `' || 'ubuntu-latest' }}`
	for _, line := range regexp.MustCompile(`(?m)^\s+runs-on:.*$`).FindAllString(workflow, -1) {
		line = strings.TrimSpace(line)
		if line != "runs-on: ubuntu-latest" && line != routed {
			t.Errorf("unrouted self-hosted job: %s", line)
		}
	}
	for _, name := range []string{"image.yml", "race-nightly.yml", "release.yml"} {
		other, err := os.ReadFile("../../../.github/workflows/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(other), "runs-on: "+Name) || strings.Contains(string(other), "self-hosted") {
			t.Errorf("%s reaches the pool", name)
		}
	}
}
