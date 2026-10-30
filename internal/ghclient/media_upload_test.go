package ghclient

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/approvals"
)

func TestApprovedMediaChecksDestinationBeforeUpload(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "gh")
	code := `#!/bin/sh
case "$1 $2" in
 "pr view") printf '%s' '{"number":1,"headRepository":{"name":"fork"},"headRepositoryOwner":{"login":"alice"}}';;
 "api --hostname") printf '%s' '{"full_name":"alice/fork","private":false,"visibility":"public"}';;
 *) echo unexpected >&2; exit 1;;
esac
`
	if err := os.WriteFile(script, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithApprovedMedia(context.Background(), approvals.MediaDestination{Host: "github.com", Base: "alice/fork", Head: "bob/fork", Number: 1, Visibility: approvals.MediaVisibilityPublic})
	cmd := Command(ctx, script, dir, "pr", "comment", "1", "--body", "hello", "--attach", "file.png")
	paths, err := stripMedia(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	_, err = uploadMedia(ctx, cmd, repository{"github.com", "alice/fork"}, "token", paths)
	if err == nil || !strings.Contains(err.Error(), "destination changed") {
		t.Fatalf("expected destination refusal, got %v", err)
	}
}

func TestApprovedMediaUploadsAndAppendsBody(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "gh")
	path := filepath.Join(dir, "image.png")
	data := []byte("verified attachment")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	name := fmt.Sprintf("%x.png", sum)
	code := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
 "pr view") echo '{"number":1,"headRepository":{"name":"repo"},"headRepositoryOwner":{"login":"alice"}}';;
 "api --hostname") case "$4" in */releases/tags/*) if test -f '%s/uploaded'; then echo '{"assets":[{"name":"%s","digest":"sha256:%s"}]}'; else echo '{"assets":[]}'; fi;; *) echo '{"full_name":"alice/repo","private":false,"visibility":"public"}';; esac;;
 "release upload") touch '%s/uploaded';;
 *) exit 1;;
esac
`, dir, name, strings.TrimSuffix(name, ".png"), dir)
	if err := os.WriteFile(script, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithApprovedMedia(context.Background(), approvals.MediaDestination{Host: "github.com", Base: "alice/repo", Head: "alice/repo", Number: 1, Visibility: approvals.MediaVisibilityPublic})
	cmd := Command(ctx, script, dir, "pr", "comment", "1", "--body", "hello", "--attach", path)
	paths, err := stripMedia(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := uploadMedia(ctx, cmd, repository{"github.com", "alice/repo"}, "token", paths)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	body, err := os.ReadFile(cmd.Args[len(cmd.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello\n\n![Media](https://github.com/alice/repo/releases/download/dyna-pr-media/"+name+")") {
		t.Fatalf("body = %s", body)
	}
}

func TestMediaRequiresApproval(t *testing.T) {
	cmd := Command(context.Background(), "gh", t.TempDir(), "pr", "comment", "1", "--attach=a.png")
	if _, err := stripMedia(context.Background(), cmd); err == nil {
		t.Fatal("attachment without approval was accepted")
	}
}

func TestMediaReleaseAndUploadRaces(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		fail       bool
	}{
		{"release created concurrently", "create", false},
		{"asset uploaded concurrently", "upload", false},
		{"conflicting concurrent asset", "mismatch", true},
		{"upload permission denied", "denied", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "image.png")
			data := []byte("approved media")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			name := fmt.Sprintf("%x.png", sum)
			digest := fmt.Sprintf("%x", sum)
			if tc.mode == "mismatch" {
				digest = "wrong"
			}
			script := filepath.Join(dir, "gh")
			code := fmt.Sprintf(`#!/bin/sh
case "$1 $2" in
 "pr view") echo '{"number":1,"headRepository":{"name":"repo"},"headRepositoryOwner":{"login":"alice"}}';;
 "api --hostname") case "$4" in
  */releases/tags/*) if test "%s" = create && ! test -f '%s/created'; then echo 'HTTP 404' >&2; exit 1; fi
   if test -f '%s/uploaded' || test "%s" = create; then echo '{"assets":[{"name":"%s","digest":"sha256:%s"}]}'; else echo '{"assets":[]}'; fi;;
  *) echo '{"full_name":"alice/repo","private":false,"visibility":"public"}';; esac;;
 "release create") touch '%s/created'; exit 1;;
 "release upload") if test "%s" != denied; then touch '%s/uploaded'; fi; exit 1;;
 *) exit 1;;
esac
`, tc.mode, dir, dir, tc.mode, name, digest, dir, tc.mode, dir)
			if err := os.WriteFile(script, []byte(code), 0700); err != nil {
				t.Fatal(err)
			}
			ctx := WithApprovedMedia(context.Background(), approvals.MediaDestination{Host: "github.com", Base: "alice/repo", Head: "alice/repo", Number: 1, Visibility: approvals.MediaVisibilityPublic})
			cmd := Command(ctx, script, dir, "pr", "comment", "1", "--body", "hello", "--attach", path)
			paths, err := stripMedia(ctx, cmd)
			if err != nil {
				t.Fatal(err)
			}
			cleanup, err := uploadMedia(ctx, cmd, repository{"github.com", "alice/repo"}, "token", paths)
			if err != nil && !tc.fail {
				t.Fatal(err)
			}
			if tc.fail && (err == nil || !strings.Contains(err.Error(), "permission")) {
				t.Fatalf("expected clear refusal, got %v", err)
			}
			cleanup()
		})
	}
}

func TestMediaRejectsVisibilityChange(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "gh")
	code := `#!/bin/sh
case "$1 $2" in
 "pr view") echo '{"number":1,"headRepository":{"name":"repo"},"headRepositoryOwner":{"login":"alice"}}';;
 "api --hostname") echo '{"full_name":"alice/repo","private":false,"visibility":"public"}';;
 *) echo "unexpected write" >&2; exit 1;;
esac
`
	if err := os.WriteFile(script, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := WithApprovedMedia(context.Background(), approvals.MediaDestination{Host: "github.com", Base: "alice/repo", Head: "alice/repo", Number: 1, Visibility: approvals.MediaVisibilityPrivate})
	cmd := Command(ctx, script, dir, "pr", "comment", "1", "--body", "hello", "--attach", "file.png")
	paths, err := stripMedia(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	_, err = uploadMedia(ctx, cmd, repository{"github.com", "alice/repo"}, "token", paths)
	if err == nil || !strings.Contains(err.Error(), "destination changed") {
		t.Fatalf("expected visibility change refusal, got %v", err)
	}
}
