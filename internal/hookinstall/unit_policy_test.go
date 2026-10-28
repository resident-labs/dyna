package hookinstall

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/processguard"
	"dyna-agent/internal/userservices"
)

func TestSelfUpdateUsesNewBinaryServicePolicy(t *testing.T) {
	for _, restartComputers := range []bool{false, true} {
		t.Run(fmt.Sprint(restartComputers), func(t *testing.T) {
			s, j := fixture(t)
			path := filepath.Join(j.Config.Units, userservices.Engine)
			if err := os.MkdirAll(j.Config.Units, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("[Service]\nSlice=dyna.slice\n"), 0600); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(j.Config.Bin, "dyna")
			delegated := false
			var systemctl []string
			s.Exec = func(ctx context.Context, p processguard.Process) error {
				if len(p.Args) > 2 && p.Args[0] == binary && p.Args[1] == "engine" && p.Args[2] == "install-services" {
					want := []string{binary, "engine", "install-services", "--unit-dir", j.Config.Units}
					if restartComputers {
						want = append(want, "--restart-computers")
					}
					if strings.Join(p.Args, "\n") != strings.Join(want, "\n") {
						t.Fatalf("service handoff = %q, want %q", p.Args, want)
					}
					delegated = true
					// Stand in for the new executable's userservices implementation.
					return userservices.Install(ctx, userservices.Options{
						Binary: binary, Dir: j.Config.Units, RestartComputers: restartComputers,
						AttemptRestart: func(context.Context) (string, error) { return "", nil },
						Environment:    p.Env, Run: func(_ context.Context, args ...string) (string, error) {
							systemctl = append(systemctl, strings.Join(args, " "))
							return "", nil
						},
					})
				}
				if len(p.Args) > 0 && p.Args[0] == "systemctl" {
					t.Fatal("old worker applied its own linked service policy")
				}
				return nil // The installed binary's engine help probe.
			}
			d := driver{s, j.Config, &bytes.Buffer{}}
			if err := d.restartServer(t.Context(), binary, restartComputers); err != nil {
				t.Fatal(err)
			}
			if !delegated {
				t.Fatal("worker did not hand service installation to the new binary")
			}
			unit, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Slice=app.slice\n", "CPUWeight=100\n", "IOWeight=100\n"} {
				if !strings.Contains(string(unit), want) {
					t.Fatalf("new policy missing %q: %s", want, unit)
				}
			}
			all := strings.Join(systemctl, "\n")
			if !strings.Contains(all, "daemon-reload") || !strings.Contains(all, "restart dyna-engine.service") {
				t.Fatalf("new installer did not apply units: %v", systemctl)
			}
		})
	}
}
