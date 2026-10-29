package ghclient

import (
	"strings"
	"testing"
)

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
