package ghclient

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerIdentityRefusedInPublicText(t *testing.T) {
	previous := ownerIdentities
	ownerIdentities = func() ([]string, error) {
		return []string{"Pat Example", "pat.example@example.invalid"}, nil
	}
	t.Cleanup(func() { ownerIdentities = previous })
	file := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(file, []byte("Opened for PAT.EXAMPLE@example.invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		args  []string
		stdin string
		want  bool
	}{
		"inline body":              {args: []string{"pr", "create", "--body", "by Pat Example"}, want: true},
		"attached title":           {args: []string{"issue", "create", "--title=Pat Example fix"}, want: true},
		"body file":                {args: []string{"pr", "create", "--repo", "a/b", "-F", file}, want: true},
		"stdin body":               {args: []string{"pr", "edit", "1", "--body-file", "-"}, stdin: "Pat Example", want: true},
		"clean body":               {args: []string{"pr", "create", "--body", "Opened by an agent"}},
		"identity as a flag value": {args: []string{"pr", "create", "--base", "Pat Example", "--body", "ok"}},
		"not a write command":      {args: []string{"pr", "view", "--json", "Pat Example"}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("gh", tt.args...)
			if tt.stdin != "" {
				cmd.Stdin = strings.NewReader(tt.stdin)
			}
			err := refuseOwnerIdentity(cmd)
			if tt.want != errors.Is(err, errOwnerIdentityInBody) || (!tt.want && err != nil) {
				t.Fatalf("error = %v, want refusal %v", err, tt.want)
			}
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "pat") {
				t.Fatalf("the refusal repeats the identity: %v", err)
			}
			if tt.stdin != "" {
				// The command still receives the body it was given.
				if got, _ := io.ReadAll(cmd.Stdin); string(got) != tt.stdin {
					t.Fatalf("stdin after the check = %q", got)
				}
			}
		})
	}
}
