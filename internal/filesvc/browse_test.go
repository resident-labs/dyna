package filesvc

import (
	"context"
	"fmt"
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

func names(b Browse) []string {
	var out []string
	for _, e := range b.Entries {
		out = append(out, e.Name)
	}
	return out
}

func TestBrowseDirsListsFoldersOnly(t *testing.T) {
	svc := &Service{}
	root := t.TempDir()
	for _, d := range []string{"beta", "Alpha", ".hidden"} {
		os.Mkdir(filepath.Join(root, d), 0o755)
	}
	gitRepo(t, filepath.Join(root, "repo"))
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644)
	os.Symlink(filepath.Join(root, "beta"), filepath.Join(root, "link-to-dir"))
	os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "link-to-file"))

	b, err := svc.BrowseDirs(root + "/")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(names(b), ","), ".hidden,Alpha,beta,link-to-dir,repo"; got != want {
		t.Fatalf("entries = %s, want %s", got, want)
	}
	for _, en := range b.Entries {
		if en.IsGit != (en.Name == "repo") {
			t.Fatalf("%s isGit = %v", en.Name, en.IsGit)
		}
	}
	if b.Parent != root {
		t.Fatalf("parent = %q", b.Parent)
	}
}

func TestBrowseDirsHiddenGitFolderStaysOut(t *testing.T) {
	svc := &Service{}
	repo := filepath.Join(t.TempDir(), "r")
	gitRepo(t, repo)
	b, err := svc.BrowseDirs(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Entries) != 0 {
		t.Fatalf("a repo lists %v", names(b))
	}
}

func TestBrowseDirsExpandsHome(t *testing.T) {
	svc := &Service{}
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.Mkdir(filepath.Join(home, "code"), 0o755)
	b, err := svc.BrowseDirs("~/")
	if err != nil || strings.Join(names(b), ",") != "code" {
		t.Fatalf("~/ = %v, %v", b, err)
	}
	if b.Parent != home {
		t.Fatalf("parent = %q", b.Parent)
	}
}

func TestBrowseDirsCapsEntries(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < MaxBrowseEntries+5; i++ {
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%04d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Test truncation independently of the UI wrapper's wall-clock timeout.
	// Other BrowseDirs tests exercise that wrapper with small folder listings.
	b, err := listDirs(context.Background(), root)
	if err != nil || len(b.Entries) != MaxBrowseEntries || !b.Truncated {
		t.Fatalf("entries %d truncated %v err %v", len(b.Entries), b.Truncated, err)
	}
	if b.Entries[0].Name != "d0000" || b.Entries[len(b.Entries)-1].Name != fmt.Sprintf("d%04d", MaxBrowseEntries-1) {
		t.Fatalf("cap did not keep the first sorted folders: %v", names(b))
	}
}

func TestBrowseDirsErrors(t *testing.T) {
	svc := &Service{}
	root := t.TempDir()
	long := "/" + strings.Repeat("a", MaxBrowsePath) // 513 characters
	if len(long) != MaxBrowsePath+1 {
		t.Fatalf("test path is %d long", len(long))
	}
	for name, path := range map[string]string{
		"relative": "some/dir",
		"empty":    "  ",
		"nul":      root + "/a\x00b",
		"513 long": long,
	} {
		if _, err := svc.BrowseDirs(path); err != ErrBadPath {
			t.Errorf("%s: err = %v, want %v", name, err, ErrBadPath)
		}
	}
	if _, err := svc.BrowseDirs(root + "/missing"); err != ErrNoFolder {
		t.Errorf("missing folder = %v", err)
	}
}

func TestBrowseDirsUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	svc := &Service{}
	dir := filepath.Join(t.TempDir(), "locked")
	os.Mkdir(dir, 0o755)
	os.Chmod(dir, 0)
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	if _, err := svc.BrowseDirs(dir); err != ErrUnreadable {
		t.Fatalf("err = %v, want %v", err, ErrUnreadable)
	}
}

func TestBrowseDirsDotDotCleans(t *testing.T) {
	svc := &Service{}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	b, err := svc.BrowseDirs(root + "/a/b/..")
	if err != nil || strings.Join(names(b), ",") != "b" {
		t.Fatalf("got %v, %v", b, err)
	}
}
