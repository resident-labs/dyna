package mode

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// New DYNA_* literals must be classified, rather than silently escaping
// fixture isolation or making a non-stamp setting disappear from a test.
func TestStampKeyClassificationCoversLiterals(t *testing.T) {
	classified := map[string]bool{}
	for _, key := range StampKeys() {
		if classified[key] {
			t.Errorf("duplicate stamp key %s", key)
		}
		classified[key] = true
	}
	for key, why := range nonStampKeys {
		if classified[key] {
			t.Errorf("%s is both a stamp and a non-stamp", key)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("non-stamp %s needs a reason", key)
		}
		classified[key] = true
	}
	for prefix, why := range nonStampPrefixes {
		if strings.TrimSpace(why) == "" {
			t.Errorf("non-stamp prefix %s needs a reason", prefix)
		}
	}
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil || !bytes.Contains(src, []byte("DYNA_")) {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				literal, ok := node.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				key, err := strconv.Unquote(literal.Value)
				if err != nil || !strings.HasPrefix(key, "DYNA_") {
					return true
				}
				// Assignments and diagnostic messages start with the same key.
				key, _, _ = strings.Cut(strings.Fields(key)[0], "=")
				if classified[key] {
					return true
				}
				for prefix := range nonStampPrefixes {
					if strings.HasPrefix(key, prefix) {
						return true
					}
				}
				t.Errorf("%s: classify %s as a stamp or give its non-stamp reason", path, key)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAgentStampKeysPreserveOwnerGuard(t *testing.T) {
	want := []string{EnvSession, EnvRunID, EnvAgentID, EnvWorkerRun, EnvAddress}
	if got := AgentStampKeys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("agent markers = %v; want %v", got, want)
	}
}

// These DYNA_* keys are not caller stamps. The contract test requires
// a reason here for each one, so fixture isolation never clears by prefix.
var nonStampKeys = map[string]string{
	"DYNA_TESTSAFE_ROOT":         "The first test binary's temporary folder, which its child test binaries keep.",
	"DYNA_FOREGROUND_SCOPE":      "A foreground cgroup, not a caller identity.",
	"DYNA_LAUNCH_GATE":           "An inherited launch-gate file descriptor, unset at process start.",
	"DYNA_GH_READ_ONLY":          "The current tool's GitHub access policy.",
	"DYNA_GH_BINARY":             "The GitHub command path.",
	"DYNA_SUBAGENT_LOCK_FD":      "A passed turn-lock file descriptor.",
	"DYNA_AGENT_JOURNAL":         "The append-only journal path.",
	"DYNA_AGENT_JOURNAL_ROOT":    "The journal folder used by a workflow runner.",
	"DYNA_ARCHIVE_DIR":           "The archive output folder.",
	"DYNA_PROFILE_CHECK_OK":      "A probe output sentinel, not an environment variable.",
	"DYNA_SHELL_JOB":             "The current managed shell job, not the Dyna caller.",
	"DYNA_COMPUTERS_DAEMON":      "A computer service marker, not a caller stamp.",
	"DYNA_JOURNAL_IDLE":          "A harness idle-notification setting.",
	"DYNA_SCRATCH":               "The worker scratch path.",
	"DYNA_STEER_HOOKS":           "Whether harness steering hooks run.",
	"DYNA_OWNER_ENV":             "The owner environment used to resolve data paths.",
	"DYNA_":                      "The namespace prefix used to filter environment entries.",
	"DYNA_CHROMIUM_EXECUTABLE":   "The Chromium executable path.",
	"DYNA_CHROMIUM_FLAGS_FILE":   "The file that supplies Chromium launch flags.",
	"DYNA_COMPUTERS_ENABLED":     "Whether the computer service is enabled.",
	"DYNA_COMPUTERS_TEST_ENABLE": "An explicit computer-service test switch.",
	"DYNA_COMPUTER_MEMORY_HIGH":  "The computer cgroup memory threshold.",
	"DYNA_COMPUTER_PROCESS":      "A computer process marker used for resource limits.",
	"DYNA_CONNECT_RELAY":         "The remote connection relay endpoint.",
	"DYNA_HOME_TOOLS":            "Tool names bridged into a private harness home.",
	"DYNA_LOCKCHECK":             "The optional lock-order diagnostics switch.",
	"DYNA_NO_AUTO_UPDATE":        "The setting that disables automatic updates.",
	"DYNA_OWNER_DISPLAY":         "The owner display used by the computer service.",
	"DYNA_OWNER_WAYLAND_DISPLAY": "The owner Wayland display used by the computer service.",
	"DYNA_PORT":                  "The primary development-server port for a worktree.",
	"DYNA_PORTS":                 "The assigned development-server ports for a worktree.",
	"DYNA_SCRIPT_PATH":           "The workflow source path supplied to its script runner.",
	"DYNA_SLICE":                 "The resource cgroup slice, not a Dyna caller identity.",
	"DYNA_BIN":                   "The path to the Dyna executable.",
}

// Hook event fields and generated MCP bridge keys describe a child command;
// they never authenticate that command as a Dyna caller.
var nonStampPrefixes = map[string]string{
	"DYNA_HOOK_":     "Hook event fields, not the process caller stamp.",
	"DYNA_MCP_":      "Generated per-server MCP execution bridge settings.",
	"DYNA_CI_CLEAN_": "Settings of the cache cleanup that a run sends to a CI machine.",
}

// A test binary drops a stamp it inherited, such as the session of the
// agent that runs go test, so no test acts as that session.
func TestTestBinaryDropsAnInheritedStamp(t *testing.T) {
	if os.Getenv("DYNA_MODE_STAMP_CHILD") == "1" {
		if got := os.Getenv(EnvSession); got != "" {
			t.Fatalf("%s = %q in a test binary; want it dropped", EnvSession, got)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestBinaryDropsAnInheritedStamp$")
	cmd.Env = append(os.Environ(), "DYNA_MODE_STAMP_CHILD=1", EnvSession+"=ses_inherited")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child test binary: %v\n%s", err, out)
	}
}
