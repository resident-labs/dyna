package ghclient

import (
	"context"
	"dyna-agent/internal/room"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestOwnerIdentityRefusedInPublicText(t *testing.T) {
	records := setupRooms(t, "fork/project")
	ctx := WithAgentText(WithRoom(context.Background(), records[0].ID))
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
		// dir runs the command in the folder that holds the relative body file.
		dir bool
		// uninspectable commands build their text where Dyna cannot read it.
		uninspectable bool
	}{
		"inline body":              {args: []string{"pr", "create", "--body", "by Pat Example"}, want: true},
		"attached title":           {args: []string{"issue", "create", "--title=Pat Example fix"}, want: true},
		"body file":                {args: []string{"pr", "create", "--repo", "a/b", "-F", file}, want: true},
		"stdin body":               {args: []string{"pr", "edit", "1", "--body-file", "-"}, stdin: "Pat Example", want: true},
		"attached short body":      {args: []string{"pr", "create", "-bPat Example"}, want: true},
		"attached short title":     {args: []string{"pr", "create", "-t=Pat Example"}, want: true},
		"relative body file":       {args: []string{"pr", "create", "--body-file", "rel.md"}, dir: true, want: true},
		"fill":                     {args: []string{"pr", "create", "--fill"}, uninspectable: true},
		"template":                 {args: []string{"issue", "create", "-T", "bug"}, uninspectable: true},
		"clean body":               {args: []string{"pr", "create", "--body", "Opened by an agent"}},
		"identity as a flag value": {args: []string{"pr", "create", "--base", "Pat Example", "--body", "ok"}},
		"not a write command":      {args: []string{"pr", "view", "--json", "Pat Example"}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("gh", tt.args...)
			if tt.stdin != "" {
				cmd.Stdin = strings.NewReader(tt.stdin)
			}
			if tt.dir {
				cmd.Dir = filepath.Dir(file)
				if err := os.WriteFile(filepath.Join(cmd.Dir, "rel.md"), []byte("Pat Example"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cleanup, err := ComposeBody(ctx, cmd, repository{"github.com", "fork/project"})
			if cleanup != nil {
				defer cleanup()
			}
			if tt.uninspectable {
				if !errors.Is(err, errUninspectableText) {
					t.Fatalf("error = %v, want the uninspectable refusal", err)
				}
				return
			}
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

// Dyna's own calls carry the owner's text, so the check leaves them alone.
func TestOwnerIdentityIgnoredForDynaCalls(t *testing.T) {
	previous := ownerIdentities
	ownerIdentities = func() ([]string, error) { return []string{"Pat Example"}, nil }
	t.Cleanup(func() { ownerIdentities = previous })
	cmd := exec.Command("gh", "pr", "edit", "1", "--body", "by Pat Example")
	if _, err := ComposeBody(context.Background(), cmd, repository{}); err != nil {
		t.Fatalf("owner edit refused: %v", err)
	}
}

func TestAttributionEndsPublishedBodies(t *testing.T) {
	records := setupRooms(t, "fork/project")
	ctx := WithAgentText(WithRoom(context.Background(), records[0].ID))
	own := repository{"github.com", "fork/project"}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "body.md"), []byte("Body text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body := func(t *testing.T, args ...string) string {
		t.Helper()
		cmd := exec.Command("gh", args...)
		cmd.Dir = dir
		cleanup, err := ComposeBody(ctx, cmd, own)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		for _, arg := range cmd.Args {
			if text, ok := strings.CutPrefix(arg, "body="); ok {
				return text
			}
			if text, ok := strings.CutPrefix(arg, "--comment="); ok {
				return text
			}
			if path, ok := strings.CutPrefix(arg, "--body-file="); ok {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				return string(data)
			}
		}
		return ""
	}
	// Dyna's own calls, such as the owner's PR edit, are never changed.
	if err := room.DefaultStore().SetAttribution(records[0].ID, "Opened by an agent."); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command("gh", "pr", "edit", "1", "--body", "Owner text")
	if _, err := ComposeBody(WithRoom(context.Background(), records[0].ID), owner, own); err != nil || owner.Args[len(owner.Args)-1] != "Owner text" {
		t.Fatalf("owner edit changed: %q, %v", owner.Args, err)
	}
	if err := room.DefaultStore().SetAttribution(records[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	// Without a setting, the command runs as given.
	if got := body(t, "pr", "create", "--body-file", "body.md"); got != "" {
		t.Fatalf("no attribution set, body file = %q", got)
	}
	if err := room.DefaultStore().SetAttribution(records[0].ID, "Opened by an agent."); err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		args []string
		want string
	}{
		"body file":      {[]string{"pr", "create", "--title", "t", "--body-file", "body.md"}, "Opened by an agent.\n\nBody text\n"},
		"inline body":    {[]string{"issue", "create", "-b", "Inline"}, "Opened by an agent.\n\nInline\n"},
		"attached body":  {[]string{"issue", "create", "--body=Inline"}, "Opened by an agent.\n\nInline\n"},
		"create no body": {[]string{"pr", "create", "--title", "t"}, "Opened by an agent.\n"},
		"already there":  {[]string{"pr", "edit", "1", "--body", "Opened by an agent.\n\nDone"}, "Opened by an agent.\n\nDone\n"},
		"edit no body":   {[]string{"pr", "edit", "1", "--title", "t"}, ""},
		"not a body":     {[]string{"pr", "view", "1"}, ""},
		"issue comment":  {[]string{"issue", "comment", "3", "--body", "Hi"}, "Opened by an agent.\n\nHi\n"},
		"issue close":    {[]string{"issue", "close", "3", "--reason", "completed", "--comment", "Done"}, "Opened by an agent.\n\nDone"},
		"issue reopen":   {[]string{"issue", "reopen", "3", "--comment=Done"}, "Opened by an agent.\n\nDone"},
		"pr comment":     {[]string{"pr", "comment", "3", "-F", "body.md"}, "Opened by an agent.\n\nBody text\n"},
		"review comment": {[]string{"pr", "review", "3", "--comment", "-b", "Fine"}, "Opened by an agent.\n\nFine\n"},
		"approve":        {[]string{"pr", "review", "3", "--approve"}, ""},
		"comment edit":   {[]string{"api", "-X", "PATCH", "repos/fork/project/issues/comments/9", "-f", "body=Hi"}, "Opened by an agent.\n\nHi"},
		"comment delete": {[]string{"api", "-X", "DELETE", "repos/fork/project/issues/comments/9"}, ""},
		// The room's own variant of the line, with other words, counts as attributed.
		"variant there": {[]string{"pr", "comment", "3", "--body", "Opened by an agent. Other words\n\nHi"}, "Opened by an agent. Other words\n\nHi\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := body(t, tt.args...); got != tt.want {
				t.Fatalf("body = %q, want %q", got, tt.want)
			}
		})
	}
	if err := room.DefaultStore().SetAttribution(records[0].ID, "> 🤖 Posted by an agent"); err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct{ body, want string }{
		"slash command":       {"/claim", "/claim\n\n> 🤖 Posted by an agent"},
		"republished command": {"/claim\n\n> 🤖 Posted by an agent", "/claim\n\n> 🤖 Posted by an agent"},
		"leading blank line":  {"\n/claim\n", "\n/claim\n\n> 🤖 Posted by an agent"},
		"agent command":       {"🤖 Agent: /claim", "🤖 Agent: /claim"},
		"quoted marker":       {"> 🤖 Opened by the swarm", "> 🤖 Opened by the swarm"},
		"ordinary":            {"Hello", "> 🤖 Posted by an agent\n\nHello"},
		"not a command":       {"/123", "> 🤖 Posted by an agent\n\n/123"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := strings.TrimSuffix(body(t, "pr", "comment", "3", "--body", tt.body), "\n"); got != tt.want {
				t.Fatalf("body = %q, want %q", got, tt.want)
			}
		})
	}
	// A marker such as "> 🤖" spans the leading punctuation and the first word.
	if got := strings.TrimSuffix(body(t, "pr", "comment", "3", "--body", "> 🤖 Opened by the swarm\n\nHi"), "\n"); got != "> 🤖 Opened by the swarm\n\nHi" {
		t.Fatalf("a variant of the line was doubled: %q", got)
	}
	if got := strings.TrimSuffix(body(t, "pr", "comment", "3", "--body", "> quote\n\nHi"), "\n"); !strings.HasPrefix(got, "> 🤖 Posted by an agent\n\n> quote") {
		t.Fatalf("a quote was taken for the line: %q", got)
	}
}

func TestAttributedCloseKeepsTerminator(t *testing.T) {
	records := setupRooms(t, "fork/project")
	if err := room.DefaultStore().SetAttribution(records[0].ID, "Opened by an agent."); err != nil {
		t.Fatal(err)
	}
	ctx := WithAgentText(WithRoom(context.Background(), records[0].ID))
	own := repository{"github.com", "fork/project"}
	for _, verb := range []string{"close", "reopen"} {
		cmd := exec.Command("gh", "issue", verb, "--comment", "Done", "--", "7")
		cmd.Dir = records[0].Repo
		cleanup, err := ComposeBody(ctx, cmd, own)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup()
		want := []string{"gh", "issue", verb, "--comment=Opened by an agent.\n\nDone", "--", "7"}
		if !slices.Equal(cmd.Args, want) {
			t.Fatalf("%s args = %q, want %q", verb, cmd.Args, want)
		}
	}
}

func TestComposeBodyPlacements(t *testing.T) {
	records := setupRooms(t, "fork/project")
	ctx := WithAgentText(WithRoom(context.Background(), records[0].ID))
	repo := repository{"github.com", "fork/project"}
	verbs := [][]string{{"issue", "create"}, {"pr", "create"}, {"issue", "edit", "1"}, {"pr", "edit", "1"}, {"issue", "comment", "1"}, {"pr", "comment", "1"}, {"issue", "close", "1"}, {"issue", "reopen", "1"}, {"pr", "review", "1", "--comment"}, {"api", "-X", "PATCH", "repos/fork/project/issues/comments/9"}, {"api", "-X", "POST", "repos/fork/project/pulls/1/reviews"}}
	for _, placement := range []room.AttributionPlacement{room.AttributionLine, room.AttributionPrefix} {
		if err := room.DefaultStore().SetAttribution(records[0].ID, "🤖 Agent:", placement); err != nil {
			t.Fatal(err)
		}
		for _, verb := range verbs {
			for _, body := range []string{"hello", "/claim", "🤖 Agent: hello", ""} {
				t.Run(strings.Join(verb, "_")+"/"+string(placement)+"/"+body, func(t *testing.T) {
					args := append([]string(nil), verb...)
					flag := "--body"
					if verb[0] == "api" {
						flag = "--raw-field"
						body = "body=" + body
					} else if closesIssue(verb) {
						flag = "--comment"
					}
					args = append(args, flag, body)
					cmd := exec.Command("gh", args...)
					cleanup, err := ComposeBody(ctx, cmd, repo)
					if err != nil {
						t.Fatal(err)
					}
					defer cleanup()
					var got string
					for _, arg := range cmd.Args {
						if v, ok := strings.CutPrefix(arg, "--body-file="); ok {
							data, err := os.ReadFile(v)
							if err != nil {
								t.Fatal(err)
							}
							got = strings.TrimSuffix(string(data), "\n")
						}
						if v, ok := strings.CutPrefix(arg, "--comment="); ok {
							got = v
						}
						if v, ok := strings.CutPrefix(arg, "body="); ok {
							got = v
						}
					}
					input := strings.TrimPrefix(body, "body=")
					want := "🤖 Agent:"
					if strings.HasPrefix(input, want) {
						want = input
					} else if input != "" {
						if placement == room.AttributionPrefix {
							want += " " + input
						} else if input == "/claim" {
							want = input + "\n\n" + want
						} else {
							want += "\n\n" + input
						}
					}
					if got != want {
						t.Fatalf("got %q, want %q", got, want)
					}
				})
			}
		}
	}
}

func TestComposeBodyRefusesMissingRoom(t *testing.T) {
	setupRooms(t, "fork/project")
	cmd := exec.Command("gh", "issue", "comment", "1", "--body", "hello")
	_, err := ComposeBody(WithAgentText(WithRoom(context.Background(), "missing")), cmd, repository{"github.com", "fork/project"})
	if err == nil {
		t.Fatal("write accepted without a readable room")
	}
}
