package roomsvc

import (
	"context"
	"dyna-agent/internal/filesvc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func TestInspectPath(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	repo := filepath.Join(root, "My Repo")
	gitRepo(t, repo)
	os.Mkdir(filepath.Join(repo, "sub"), 0o755)
	os.Mkdir(filepath.Join(root, "plain"), 0o755)

	chk, err := e.svc.InspectPath(repo + "/sub")
	real, _ := filepath.EvalSymlinks(repo)
	if err != nil || chk.State != PathRepo || chk.Repo != real || chk.Name != "my-repo" || chk.RoomID != "" {
		t.Fatalf("repo subfolder = %+v, %v", chk, err)
	}
	if chk, err = e.svc.InspectPath(filepath.Join(root, "plain")); err != nil || chk.State != PathNotGit || chk.Name != "plain" {
		t.Fatalf("plain = %+v, %v", chk, err)
	}
	if chk, err = e.svc.InspectPath(filepath.Join(root, "new", "deep")); err != nil || chk.State != PathMissing {
		t.Fatalf("missing = %+v, %v", chk, err)
	}
	made, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: repo})
	if err != nil {
		t.Fatal(err)
	}
	if chk, _ = e.svc.InspectPath(repo); chk.RoomID != made.ID || chk.RoomName != "my-repo" {
		t.Fatalf("existing room not reported: %+v", chk)
	}
}

func TestCreateRoomFromRepo(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(t.TempDir(), "proj")
	gitRepo(t, repo)
	os.Mkdir(filepath.Join(repo, "sub"), 0o755)
	real, _ := filepath.EvalSymlinks(repo)

	got, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: repo + "/sub"})
	if err != nil || got.Existed || got.Name != "proj" || got.Repo != real {
		t.Fatalf("create = %+v, %v", got, err)
	}
	// Same repo again, even with another name, opens and revives the same room.
	if err := e.svc.rooms().SetArchived(got.ID, true); err != nil {
		t.Fatal(err)
	}
	again, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: repo, Name: "other"})
	if err != nil || !again.Existed || again.ID != got.ID {
		t.Fatalf("second create = %+v, %v", again, err)
	}
	rec, err := e.svc.rooms().Resolve(got.ID)
	if err != nil || rec.ManualArchive {
		t.Fatalf("reused room = %+v, %v; want revived room", rec, err)
	}

}

func TestCreateRoomCustomName(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(t.TempDir(), "proj")
	gitRepo(t, repo)
	got, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: repo, Name: " my-name "})
	if err != nil || got.Name != "my-name" {
		t.Fatalf("create = %+v, %v", got, err)
	}
	other := filepath.Join(t.TempDir(), "two")
	gitRepo(t, other)
	if _, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: other, Name: "Bad Name"}); err == nil {
		t.Fatal("an invalid room name must fail")
	}
}

func TestCreateRoomNotGit(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(t.TempDir(), "plain")
	os.Mkdir(dir, 0o755)
	if _, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: dir}); err != ErrNotGit {
		t.Fatalf("err = %v, want %v", err, ErrNotGit)
	}
	got, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: dir, GitInit: true})
	if err != nil || got.Name != "plain" {
		t.Fatalf("with git init = %+v, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		t.Fatal("git init did not run")
	}
}

func TestCreateRoomMakesMissingFolders(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "fresh")
	if _, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: path}); err != filesvc.ErrNoFolder {
		t.Fatalf("without GitInit err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a")); err == nil {
		t.Fatal("a failed request must not create folders")
	}
	got, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: path, GitInit: true})
	_, gitErr := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || got.Name != "fresh" || gitErr != nil {
		t.Fatalf("create = %+v, %v", got, err)
	}
}

func TestCreateRoomLinkedWorktreeUsesMainRepo(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	main := filepath.Join(root, "main")
	gitRepo(t, main)
	for _, args := range [][]string{
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-qm", "x"},
		{"worktree", "add", "-q", filepath.Join(root, "wt")},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = main
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	real, _ := filepath.EvalSymlinks(main)
	got, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: filepath.Join(root, "wt")})
	if err != nil || got.Repo != real || got.Name != "main" {
		t.Fatalf("worktree = %+v, %v", got, err)
	}
	rec, err := e.svc.rooms().Resolve(got.ID)
	if err != nil || rec.Repo != real {
		t.Fatalf("stored = %+v, %v", rec, err)
	}
}

func TestCreateRoomBadPaths(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"", "rel/path", "/a\x00b", "/" + strings.Repeat("x", filesvc.MaxBrowsePath)} {
		if _, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: p, GitInit: true}); err == nil {
			t.Errorf("CreateRoom(%q) succeeded", p)
		}
	}
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, nil, 0o644)
	if _, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: file, GitInit: true}); err == nil {
		t.Error("a file must not make a room")
	}
}

func TestCreateRoomReusesRepoWithReservedName(t *testing.T) {
	for _, name := range []string{"global", "room", "___"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			repo := filepath.Join(t.TempDir(), name)
			gitRepo(t, repo)
			first, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: repo, Name: "custom"})
			if err != nil {
				t.Fatal(err)
			}
			if err := e.svc.rooms().SetArchived(first.ID, true); err != nil {
				t.Fatal(err)
			}
			again, err := e.svc.CreateRoom(context.Background(), CreateRoomInput{Path: repo})
			if err != nil || !again.Existed || again.ID != first.ID || again.Name != "custom" {
				t.Fatalf("reuse = %+v, %v; want %s", again, err, first.ID)
			}
			stored, err := e.svc.rooms().Resolve(first.ID)
			if err != nil || stored.ManualArchive {
				t.Fatalf("reused room = %+v, %v; want revived room", stored, err)
			}
		})
	}
}
