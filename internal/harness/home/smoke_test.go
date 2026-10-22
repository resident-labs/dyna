package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This opt-in smoke uses installed tools without recording their environment or
// output. The report contains paths only. Normal package tests use fake tools.
func TestInstalledToolSmoke(t *testing.T) {
	report := os.Getenv("HARNESSHOME_SMOKE_REPORT")
	if report == "" {
		t.Skip("set HARNESSHOME_SMOKE_REPORT to run installed tool smoke")
	}
	env := os.Environ()
	scope := filepath.Join(t.TempDir(), "native")
	out, err := Prepare(scope, "/fake/dyna", env)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{out["HOME"], out["DYNA_BIN"], out["GIT_CONFIG_GLOBAL"]}
	for _, tool := range []struct {
		name string
		args []string
	}{{"go", []string{"version"}}, {"cargo", []string{"--version"}}, {"node", []string{"-v"}}, {"mise", []string{"exec", "--", "node", "-v"}}} {
		t.Run(tool.name, func(t *testing.T) {
			executable, err := exec.LookPath(tool.name)
			if err != nil {
				t.Fatal(err)
			}
			run(t, merged(env, out), executable, tool.args...)
			paths = append(paths, executable)
		})
	}
	if err := os.WriteFile(report, []byte(strings.Join(paths, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
