package podman

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func quadletSpec(t *testing.T) box.ServiceUnitSpec {
	t.Helper()
	parts, err := contract.ServiceSpec{
		Name: "relay", Image: "ghcr.io/o/r@sha256:" + strings.Repeat("a", 64), Ports: []contract.ServicePort{"443", "8443:9000/udp"},
		Env: map[string]string{"MODE": "prod", "PCT": "100%$HOME"}, Secrets: []string{"TOKEN"}, Data: "/var/lib/relay",
		CPUs: 1.5, Memory: "512MiB", Disk: "1GiB", Command: []string{"/relay", "--serve"},
	}.Parse()
	if err != nil {
		t.Fatal(err)
	}
	return box.ServiceUnitSpec{ID: "svc_1", Room: "room_1", Parts: parts, Image: parts.Digest}
}

// quadletVariants are goldens beside the main one: a room image with on-failure restart and no data volume, and values that would
// break out of a line or a word if they were not escaped.
func quadletVariants(t *testing.T) map[string]box.ServiceUnitSpec {
	t.Helper()
	parse := func(spec contract.ServiceSpec) contract.ServiceParts {
		parts, err := spec.Parse()
		if err != nil {
			t.Fatal(err)
		}
		return parts
	}
	return map[string]box.ServiceUnitSpec{
		"service.container":    quadletSpec(t),
		"room-image.container": {ID: "svc_2", Room: "room_1", Image: "localhost/dyna-img-app:1", Parts: parse(contract.ServiceSpec{Name: "app", Image: "app", Restart: contract.ServiceRestartOnFailure})},
		"hostile.container": {ID: "svc_3", Room: "room_1", Image: "localhost/dyna-img-app:1", Parts: parse(contract.ServiceSpec{Name: "app", Image: "app",
			Env: map[string]string{"A": "x\" Exec=/bin/sh", "B": "${HOME} %h \\"}, Command: []string{"run", "a b", "$X", "%n"}})},
	}
}

func TestServiceQuadletGolden(t *testing.T) {
	for name, spec := range quadletVariants(t) {
		got := RenderServiceUnit(spec)
		if os.Getenv("GEN") != "" {
			if err := os.WriteFile("testdata/"+name, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if got != string(want) {
			t.Errorf("quadlet differs from testdata/%s:\n%s", name, got)
		}
	}
}

// Each isolation promise of 5.18 is a line of the quadlet, and no line grants more.
func TestServiceQuadletIsolationLines(t *testing.T) {
	file := RenderServiceUnit(quadletSpec(t))
	for _, line := range []string{"UserNS=nomap", "Network=pasta:--no-map-gw", "DropCapability=all", "NoNewPrivileges=true", "ReadOnly=true", "ReadOnlyTmpfs=false", "PodmanArgs=--image-volume=ignore", "Slice=dyna-svc.slice"} {
		if !strings.Contains(file, "\n"+line+"\n") {
			t.Errorf("no line %q", line)
		}
	}
	for _, banned := range []string{"AddCapability", "Network=host", "Network=slirp4netns", "UserNS=host", "Privileged", "SecurityLabelDisable", "PidsLimit=0", "AddDevice", "Mount=", "/run/podman", "ReadOnly=false", "Unmask", "Pod="} {
		if strings.Contains(file, banned) {
			t.Errorf("quadlet holds %q", banned)
		}
	}
	var volumes []string
	for _, line := range strings.Split(file, "\n") {
		if strings.HasPrefix(line, "Volume=") {
			volumes = append(volumes, line)
		}
	}
	if len(volumes) != 1 || volumes[0] != "Volume=dyna-svc-svc_1:/var/lib/relay" {
		t.Fatalf("volumes %v", volumes)
	}
}

func TestServiceQuadletHoldsNoSecretValue(t *testing.T) {
	spec := quadletSpec(t)
	file := RenderServiceUnit(spec)
	if !strings.Contains(file, "Secret=dyna-svc-svc_1-TOKEN,type=env,target=TOKEN") {
		t.Fatal("secret is not named by reference")
	}
}

type recordRunner struct {
	args  [][]string
	stdin [][]byte
}

func (r *recordRunner) Run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	r.args = append(r.args, args)
	r.stdin = append(r.stdin, stdin)
	return []byte("DYNA-END 00000000000000000000 000\n"), nil
}

// A secret value reaches the host on stdin of one command. The arguments of every command, which end up in process lists
// and logs, hold the secret's name only.
func TestSecretValueTravelsOnStdinNeverInArgv(t *testing.T) {
	r := &recordRunner{}
	h, err := New("user@host", t.TempDir(), "host", r)
	if err != nil {
		t.Fatal(err)
	}
	const value = "s3cr3t-value-0123"
	ctx := context.Background()
	if err := h.WriteServiceSecret(ctx, "svc_1", "TOKEN", []byte(value)); err != nil {
		t.Fatal(err)
	}
	spec := quadletSpec(t)
	if _, err := h.WriteServiceUnit(ctx, spec); err != nil {
		t.Fatal(err)
	}
	onStdin := 0
	for i, args := range r.args {
		if strings.Contains(strings.Join(args, " "), value) {
			t.Fatalf("value in argv: %v", args)
		}
		if strings.HasSuffix(string(r.stdin[i]), value) {
			onStdin++
		} else if strings.Contains(string(r.stdin[i]), value) {
			t.Fatalf("value inside a file: %q", r.stdin[i])
		}
	}
	if onStdin != 1 {
		t.Fatalf("the value travelled on stdin of %d commands, want 1", onStdin)
	}
}
