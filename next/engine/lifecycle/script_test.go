package lifecycle

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
)

func writeScript(t *testing.T, text string) Script {
	t.Helper()
	s := Script{Layout: box.Layout{Data: t.TempDir()}}
	if err := os.MkdirAll(s.Layout.Pool("boats"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Layout.PoolScript("boats"), []byte(text), 0o700); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScriptGetsItsArgumentsAndNoSecrets(t *testing.T) {
	t.Setenv("GH_TOKEN", "hunter2")
	s := writeScript(t, `#!/bin/sh
env > env.txt
echo "$1 $2 $3" > args.txt
pwd > dir.txt
echo '{"ssh": "pip@10.0.0.1:2222"}'
`)
	ssh, err := s.Create(context.Background(), "boats", "boats-1", "medium")
	if err != nil || ssh != "ssh://pip@10.0.0.1:2222" {
		t.Fatalf("%q %v", ssh, err)
	}
	read := func(name string) string {
		data, err := os.ReadFile(s.Layout.Pool("boats") + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if got := read("env.txt"); strings.Contains(got, "hunter2") {
		t.Fatalf("the script saw a secret: %s", got)
	}
	if got := read("args.txt"); got != "create boats-1 medium\n" {
		t.Fatalf("%q", got)
	}
	if got := read("dir.txt"); strings.TrimSpace(got) != s.Layout.Pool("boats") {
		t.Fatalf("%q", got)
	}
}

func TestScriptReportsStderrAndParsesStdout(t *testing.T) {
	ctx := context.Background()
	failing := writeScript(t, "#!/bin/sh\necho out of boats >&2\nexit 3\n")
	if _, err := failing.Create(ctx, "boats", "n", "agent"); err == nil || !strings.Contains(err.Error(), "out of boats") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatal(err)
	}
	noise := writeScript(t, "#!/bin/sh\necho booting\n")
	if _, err := noise.Create(ctx, "boats", "n", "agent"); err == nil {
		t.Fatal("stdout that is not JSON was taken")
	}
	if _, err := noise.List(ctx, "boats"); err == nil {
		t.Fatal("stdout that is not JSON was taken")
	}
	list := writeScript(t, "#!/bin/sh\necho '[\"a\", \"b\"]'\n")
	if names, err := list.List(ctx, "boats"); err != nil || len(names) != 2 {
		t.Fatalf("%v %v", names, err)
	}
	empty := writeScript(t, "#!/bin/sh\necho '[]'\n")
	if names, err := empty.List(ctx, "boats"); err != nil || len(names) != 0 {
		t.Fatalf("%v %v", names, err)
	}
}

func TestScriptStopsAtItsDeadlineWithItsChildren(t *testing.T) {
	s := writeScript(t, "#!/bin/sh\nsleep 60 &\nwait\n")
	began := time.Now()
	if _, err := s.run(context.Background(), "boats", 50*time.Millisecond, "create"); err == nil {
		t.Fatal("a hung script returned")
	}
	if elapsed := time.Since(began); elapsed > 5*time.Second {
		t.Fatalf("took %s", elapsed)
	}
}

func TestSSHDestination(t *testing.T) {
	for text, want := range map[string]string{
		"pip@host":       "pip@host",
		"pip@host:2222":  "ssh://pip@host:2222",
		"pip@[::1]:2222": "ssh://pip@[::1]:2222",
		"":               "",
		"host":           "",
		"-oProxy=x@host": "",
		"pip@host now":   "",
		"pip@a@b":        "",
		"ssh://pip@host": "",
	} {
		got, err := sshDestination(text)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("%q: %q %v", text, got, err)
		}
	}
}
