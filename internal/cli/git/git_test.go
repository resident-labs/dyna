package git

import (
	"bytes"
	"strings"
	"testing"
)

func TestGroup(t *testing.T) {
	cmd := NewCommand()
	if cmd.Name() != "git" || cmd.Short == "" {
		t.Fatalf("invalid git group: %+v", cmd)
	}
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshAndExportCommands(t *testing.T) {
	for _, args := range [][]string{{"sync", "refresh"}, {"export"}} {
		cmd, _, err := NewCommand().Find(args)
		if err != nil || cmd == nil || cmd.Flags().Lookup("wait") == nil {
			t.Fatalf("%v command=%v error=%v", args, cmd, err)
		}
	}
}

func TestMergeHelp(t *testing.T) {
	cmd := NewCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"merge", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"git merge", "status", "pause", "resume", "cancel", "return", "retry", "bisect", "publish", "--room"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("merge help misses %q: %s", want, out.String())
		}
	}
}
