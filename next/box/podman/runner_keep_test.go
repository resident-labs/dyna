package podman

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestStartRunnerMountsOnlyItsRepositorysKeep(t *testing.T) {
	h, runner := newFrameHost(t)
	for i, test := range []struct {
		repo contract.RepoName
		key  string
	}{
		{"a/x", "a+x"}, {"b/x", "b+x"}, {"a/x", "a+x"},
		{"a-b/x", "a-b+x"}, {"a/b-x", "a+b-x"},
	} {
		s := testSpec()
		s.Repo, s.Slot = test.repo, i
		if err := h.StartRunner(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		command := strings.Join(runner.args[len(runner.args)-1], " ")
		want := "type=bind,source=" + filepath.Join(h.dataDir, "ci-keep", test.key) + ",destination=/keep"
		if strings.Count(command, ",destination=/keep") != 1 || !strings.Contains(command, "'"+want+"'") ||
			!strings.Contains(command, "'DYNA_CI_KEEP=/keep'") {
			t.Errorf("%s did not get only its writable keep mount: %s", s.Repo, command)
		}
	}
}

func TestKeepOverCapWaitsForOtherRunnersOfTheRepo(t *testing.T) {
	for _, state := range []string{"idle", "held", "other-repo", "listing-failed"} {
		t.Run(state, func(t *testing.T) {
			keep := filepath.Join(t.TempDir(), "ci-keep", "one+repo")
			if err := os.MkdirAll(keep, 0755); err != nil {
				t.Fatal(err)
			}
			proof := filepath.Join(keep, "proof")
			if err := os.WriteFile(proof, []byte("keep"), 0644); err != nil {
				t.Fatal(err)
			}
			tools := fakeTools(t, map[string]string{"podman": `case "$1" in
ps)
  case "$*" in
  *label=dyna.ci.repo=one/repo*) ;;
  *) exit 9 ;;
  esac
  case "$STATE" in
  held) echo id ;;
  listing-failed) exit 5 ;;
  esac ;;
unshare) shift; [ "$1" = chown ] || exec "$@" ;;
*) exit 9 ;;
esac`})
			cmd := exec.Command("bash", "-c", keepScript, "test", keep, "one/repo", "0")
			cmd.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"), "STATE="+state)
			if out, err := cmd.CombinedOutput(); (err != nil) != (state == "listing-failed") {
				t.Fatalf("unexpected result: %v: %s", err, out)
			}
			_, err := os.Stat(proof)
			if wantGone := state == "idle" || state == "other-repo"; os.IsNotExist(err) != wantGone {
				t.Errorf("proof stat: %v (want deleted: %v)", err, wantGone)
			}
		})
	}
}

func TestKeepCountsAgainstTheBuildShare(t *testing.T) {
	for _, keepFits := range []bool{true, false} {
		root := t.TempDir()
		build, keep := filepath.Join(root, "ci-build", "0"), filepath.Join(root, "ci-keep")
		if err := os.MkdirAll(filepath.Join(build, "target"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(keep, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(build+".repo", []byte("owner/repo\n"), 0644); err != nil {
			t.Fatal(err)
		}
		proof := filepath.Join(build, "target", "proof")
		if err := os.WriteFile(proof, nil, 0644); err != nil {
			t.Fatal(err)
		}
		// A 1,000 GiB disk gives each of 16 slots 50 GiB before keep. A 16 GiB keep reduces that to 49 GiB.
		tools := fakeTools(t, map[string]string{
			"df": `printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/x 1048576000 0 900000000 10%% /\n'`,
			"podman": `[ "$1" = unshare ] || exit 9; shift
case "$1" in
chown) exit 0 ;;
du)
  case "$4" in
  */ci-keep) n=$KEEP_SIZE ;;
  *) if [ -f "$4/target/proof" ]; then n=51904512; else n=1; fi ;;
  esac
  printf '%s\t%s\n' "$n" "$4" ;;
*) exec "$@" ;;
esac`,
		})
		keepSize := "16777216"
		if keepFits {
			keepSize = "0"
		}
		cmd := exec.Command("bash", "-c", buildScript, "test", build, "owner/repo", "83886080", "16", filepath.Join(root, "cache"), keep)
		cmd.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"), "KEEP_SIZE="+keepSize)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		if _, err := os.Stat(proof); (err == nil) != keepFits {
			t.Errorf("proof stat: %v (want preserved: %v)", err, keepFits)
		}
	}
}

func TestPruneKeepAfterSlotsAndWhenCIIsDisabled(t *testing.T) {
	for _, scenario := range []string{"roomy", "short", "listing-failed"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			build, keep := filepath.Join(root, "ci-build"), filepath.Join(root, "ci-keep")
			for _, folder := range []string{filepath.Join(build, "0"), filepath.Join(keep, "active+repo"), filepath.Join(keep, "old+repo"), filepath.Join(keep, "idle+repo"), filepath.Join(keep, "z+repo")} {
				if err := os.MkdirAll(folder, 0755); err != nil {
					t.Fatal(err)
				}
			}
			tools := fakeTools(t, map[string]string{
				"df": `free=900000000
if [ "$SCENARIO" = short ] && { [ -d "$ROOT/ci-build/0" ] || [ -d "$ROOT/ci-keep/idle+repo" ]; }; then free=150000000; fi
printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/x 1048576000 0 %s 85%% /\n' "$free"`,
				"podman": `case "$1" in
ps)
  case "$*" in
  *--all*label=dyna.ci.repo*)
    [ "$SCENARIO" != listing-failed ] || exit 5
    echo active/repo ;;
  *--all*label=dyna.ci.slot*) ;;
  *) exit 9 ;;
  esac ;;
unshare)
  shift
  case "$*" in
  *ci-keep/idle+repo*) [ ! -d "$ROOT/ci-build/0" ] || exit 9 ;;
  esac
  exec "$@" ;;
*) exit 9 ;;
esac`,
			})
			cmd := exec.Command("bash", "-c", pruneRunnerFoldersScript, "test", build, "16", "", "", keep, "check", "idle/repo", "z/repo")
			cmd.Env = append(os.Environ(), "PATH="+tools+":"+os.Getenv("PATH"), "ROOT="+root, "SCENARIO="+scenario)
			if out, err := cmd.CombinedOutput(); (err != nil) != (scenario == "listing-failed") {
				t.Fatalf("unexpected result: %v: %s", err, out)
			}
			for folder, wantGone := range map[string]bool{
				"ci-build/0":          scenario == "short",
				"ci-keep/idle+repo":   scenario == "short",
				"ci-keep/active+repo": false,
				"ci-keep/old+repo":    scenario != "listing-failed",
				"ci-keep/z+repo":      false,
			} {
				if _, err := os.Stat(filepath.Join(root, folder)); errors.Is(err, os.ErrNotExist) != wantGone {
					t.Errorf("%s stat: %v (want deleted: %v)", folder, err, wantGone)
				}
			}
		})
	}
}
