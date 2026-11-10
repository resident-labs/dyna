package podman

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func TestPushCacheUsesMaster(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	for i := 0; i < 2; i++ {
		before := len(r.calls)
		if err := h.PushCache(context.Background(), "/mirror", "/data/git/owner/repo.git", []string{"refs/heads/main"}); err != nil {
			t.Fatal(err)
		}
		var init, push []string
		for _, call := range r.calls[before:] {
			if len(call) > 0 && strings.Contains(call[len(call)-1], `git init --bare`) {
				init = call
			}
			if len(call) > 0 && call[0] == "env" {
				push = call
			}
		}
		if len(init) == 0 || !strings.HasSuffix(init[len(init)-1], `'dyna' '/data/git/owner/repo.git' '' '.dyna-github-identity'`) || !strings.Contains(init[len(init)-1], `rm -rf -- "$1"`) {
			t.Fatalf("cache reset/init: %v", init)
		}
		want := []string{"env", "GIT_SSH_COMMAND=ssh -S " + quote(h.socket) + " -o ControlMaster=no", "git", "-C", "/mirror", "push", "--force", "user@host:/data/git/owner/repo.git", "refs/heads/main"}
		if !reflect.DeepEqual(push, want) {
			t.Fatalf("push: %v", push)
		}
	}
	if err := h.PushCache(context.Background(), "/mirror", "/data:/repo.git", nil); err == nil {
		t.Fatal("accepted a path that git would read as a host")
	}
}

func TestCacheProvenanceResetsOldObjects(t *testing.T) {
	for _, markers := range [][2]string{{`{"login":"","generation":"one"}`, `{"login":"work","generation":"two"}`}, {`{"login":"work","generation":"one"}`, `{"login":"","generation":"two"}`}, {`{"login":"work","generation":"one"}`, `{"login":"work","generation":"refilled"}`}} {
		cache := filepath.Join(t.TempDir(), "repo.git")
		if err := os.MkdirAll(cache, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cache, box.GitIdentityMarker), []byte(markers[0]), 0600); err != nil {
			t.Fatal(err)
		}
		oldObject := filepath.Join(cache, "old-private-object")
		if err := os.WriteFile(oldObject, []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("bash", "-c", prepareGitCache, "dyna", cache, markers[1], box.GitIdentityMarker)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("reset: %v %s", err, output)
		}
		if _, err := os.Stat(oldObject); !os.IsNotExist(err) {
			t.Fatal("previous cache objects survived identity/refill change")
		}
	}
}

func TestCreateContainerCreatesWritableMount(t *testing.T) {
	r := &runner{}
	h := newTestHost(t, r)
	err := h.CreateContainer(context.Background(), box.ContainerSpec{Box: "a", Image: "image", Mounts: []box.Mount{{HostPath: "/cache", BoxPath: "/box/cache"}}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := r.calls[1][len(r.calls[1])-1]
	if !strings.Contains(cmd, `mkdir -p -m 0700 -- "$1"`) || !strings.HasSuffix(cmd, "'dyna' '/cache' ''") {
		t.Fatal(cmd)
	}
}

// Exercise the driver's SSH framing and shell quoting with a fake Podman. The fake
// models image ENV and Podman's --env/--unsetenv on a real process environment;
// it does not stand in for an integration test with Podman and a CDI GPU.
func TestCreateContainerVulkanEnvironment(t *testing.T) {
	zero, other := 0, 3
	for _, tc := range []struct {
		name   string
		card   *int
		device string
	}{
		{"without GPU", nil, ""},
		{"GPU zero", &zero, "nvidia.com/gpu=0"},
		{"GPU nonzero", &other, "nvidia.com/gpu=3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argsFile, envFile := filepath.Join(dir, "args"), filepath.Join(dir, "env")
			const lavapipe = "/usr/share/vulkan/icd.d/lvp_icd.x86_64.json"
			tools := fakeTools(t, map[string]string{"podman": `
printf '%s\000' "$@" > ` + quote(argsFile) + `
export VK_ICD_FILENAMES=` + quote(lavapipe) + `
export IMAGE_OTHER=keep-me
# Podman removes defaults before applying explicit --env values.
for arg in "$@"; do
    case "$arg" in
        --unsetenv=*) unset "${arg#--unsetenv=}" ;;
        image) break ;;
    esac
done
while [ "$#" -gt 0 ]; do
    case "$1" in
        --env) export "$2"; shift ;;
        image) break ;;
    esac
    shift
done
env -0 > ` + quote(envFile)})
			h, err := New("user@host", t.TempDir(), "host", localShell{path: tools + ":" + os.Getenv("PATH")}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			const value = "value with spaces and 'quotes'"
			if err := h.CreateContainer(context.Background(), box.ContainerSpec{Box: "a", Image: "image", GPUCard: tc.card, Env: []box.EnvVar{{Name: "BOX_OTHER", Value: value}}}); err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(argsFile)
			if err != nil {
				t.Fatal(err)
			}
			wantArgs := []string{"--cgroup-manager=systemd", "create", "--name", "a", boxUserNS,
				"--user", "1000:1000", "--label", "dyna.box=a", "--cgroup-parent=", "--cgroupns=private",
				"--systemd=always", "--stop-signal", "SIGTERM", "--entrypoint", "/usr/bin/tini",
				"--tmpfs", "/run/dyna:mode=1777", "--env", "HOME=/box/home", "--env", "PI_CODING_AGENT_DIR=/box/pi",
				"--volume", "dyna-box-a:/box", "--env", "BOX_OTHER=" + value}
			if tc.card != nil {
				wantArgs = append(wantArgs, "--unsetenv=VK_ICD_FILENAMES", "--device", tc.device)
			}
			wantArgs = append(wantArgs, "image", "--", "/opt/dyna/bin/current/dyna", "box-agent")
			if got := strings.Split(strings.TrimSuffix(string(args), "\x00"), "\x00"); !reflect.DeepEqual(got, wantArgs) {
				t.Errorf("Podman argv = %q, want %q", got, wantArgs)
			}
			env, err := os.ReadFile(envFile)
			if err != nil {
				t.Fatal(err)
			}
			vars := map[string]string{}
			for _, entry := range strings.Split(string(env), "\x00") {
				if name, value, ok := strings.Cut(entry, "="); ok {
					vars[name] = value
				}
			}
			icd, present := vars["VK_ICD_FILENAMES"]
			if tc.card != nil && present || tc.card == nil && (!present || icd != lavapipe) {
				t.Errorf("VK_ICD_FILENAMES = %q (present %t), GPU = %v", icd, present, tc.card)
			}
			if vars["IMAGE_OTHER"] != "keep-me" || vars["BOX_OTHER"] != value || vars["HOME"] != "/box/home" || vars["PI_CODING_AGENT_DIR"] != "/box/pi" {
				t.Errorf("unrelated environment changed: %v", vars)
			}
		})
	}
}
