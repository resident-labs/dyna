package ghclient

import (
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
)

func TestDisplayApprovalBodyUsesBoundFileSnapshot(t *testing.T) {
	for _, argv := range [][]string{{"pr", "create", "--body-file", "body.md"}, {"pr", "create", "--body-file=body.md"}, {"pr", "create", "-Fbody.md"}} {
		payload := approvals.GHPayload{Argv: argv, Files: []approvals.FileSnapshot{{Path: "body.md", Data: []byte("approved body")}}}
		if got := DisplayApprovalBody(payload); got != "approved body" {
			t.Fatalf("body = %q for %q", got, argv)
		}
	}
}

func TestDisplayApprovalBodyUsesEveryBoundAPIField(t *testing.T) {
	payload := approvals.GHPayload{
		Argv: []string{"api", "repos/o/r/issues", "--input=payload.json", "-iFtitle=@title.txt", "-F", "body=@@body.txt"},
		Files: []approvals.FileSnapshot{
			{Path: "payload.json", Data: []byte("{\"body\":\"input\"}")},
			{Path: "title.txt", Data: []byte("snapshot title")},
			{Path: "@body.txt", Data: []byte("snapshot body")},
		},
	}
	got := DisplayApprovalBody(payload)
	for _, want := range []string{"{\"body\":\"input\"}", "title:\nsnapshot title", "body:\nsnapshot body"} {
		if !strings.Contains(got, want) {
			t.Fatalf("display %q does not contain %q", got, want)
		}
	}
}

func TestRewriteFileArgsCoversEveryFileBackedArgument(t *testing.T) {
	tests := []struct {
		name string
		args []string
		path string
	}{
		{name: "body", args: []string{"pr", "create", "--body-file", "body.md"}, path: "body.md"},
		{name: "body short", args: []string{"pr", "create", "-Fbody.md"}, path: "body.md"},
		{name: "api field", args: []string{"api", "repos/o/r/issues", "-F", "body=@body.md"}, path: "body.md"},
		{name: "api field attached", args: []string{"api", "repos/o/r/issues", "--field=body=@body.md"}, path: "body.md"},
		{name: "api field at name", args: []string{"api", "repos/o/r/issues", "-F", "body=@@name"}, path: "@name"},
		{name: "api grouped field", args: []string{"api", "repos/o/r/issues", "-iFtitle=@title.txt"}, path: "title.txt"},
		{name: "api input", args: []string{"api", "repos/o/r/issues", "--input=payload.json"}, path: "payload.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, err := RewriteFileArgs(tt.args, map[string]string{tt.path: "/snapshot"})
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(argv, " ")
			if !strings.Contains(joined, "/snapshot") || strings.Contains(joined, tt.path) {
				t.Fatalf("argv = %q", joined)
			}
		})
	}
}
