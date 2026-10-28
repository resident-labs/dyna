package runtime

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// cliStart reports whether args address this binary as a command rather than
// as a test run. The Pi OAuth sign-in helper builds such an argv from
// os.Executable (piOAuthArgv); under go test that is this binary, and one
// unguarded start would re-run the whole suite as its own child process.
func cliStart(args []string) bool {
	return len(args) > 1 && !strings.HasPrefix(args[1], "-test.")
}

// TestMain refuses CLI arguments before any test runs, so a test that reaches
// a sign-in or helper argv cannot multiply test processes.
func TestMain(m *testing.M) {
	if cliStart(os.Args) {
		fmt.Fprintf(os.Stderr, "refusing CLI arguments; this is the test binary: %s\n", os.Args[1])
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestTestBinaryRefusesCLIArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"sign-in helper argv", []string{"runtime.test", "harness", "accounts", "_pi-signin", "--pi", "/fake/pi"}, true},
		{"any command word", []string{"runtime.test", "serve"}, true},
		{"bare run", []string{"runtime.test"}, false},
		{"filtered run", []string{"runtime.test", "-test.run=^TestX$"}, false},
		{"verbose run", []string{"runtime.test", "-test.v=true", "-test.paniconexit0"}, false},
		{"bench run", []string{"runtime.test", "-test.bench=."}, false},
	} {
		if got := cliStart(tc.args); got != tc.want {
			t.Errorf("%s: cliStart(%q) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}
