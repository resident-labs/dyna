package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/compose"
	"github.com/resident-labs/dyna/engine/images"
)

const composeRepo contract.RepoName = "owner/repo"

// gitIn runs git in dir with a fixed identity.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "files")
}

// mirrorOf makes a real mirror of a real repository, as a box start leaves one.
func mirrorOf(t *testing.T, build func(dir string)) *Mirror {
	t.Helper()
	source := t.TempDir()
	gitIn(t, source, "init", "-q", "-b", "main")
	build(source)
	data := t.TempDir()
	layout := box.Layout{Data: data, HostData: "/host"}
	path, err := layout.Git(composeRepo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, data, "clone", "-q", "--bare", source, path)
	return &Mirror{Layout: layout, Runner: GitRunner{}}
}

func TestComposeFilesComeFromTheDefaultBranch(t *testing.T) {
	m := mirrorOf(t, func(dir string) {
		commitFiles(t, dir, map[string]string{".dyna/compose/default.yaml": "a", ".dyna/compose/other.yaml": "b", ".dyna/compose/README.md": "no", ".dyna/compose/Not A Name.yaml": "no"})
		if err := os.Symlink("/etc/passwd", filepath.Join(dir, ".dyna/compose/link.yaml")); err != nil {
			t.Fatal(err)
		}
		commitFiles(t, dir, map[string]string{"x": "y"})
	})
	ctx := context.Background()
	names, err := m.ComposeNames(ctx, composeRepo)
	if err != nil || strings.Join(names, " ") != "default other" {
		t.Fatalf("names %v: %v", names, err)
	}
	data, err := m.ComposeFile(ctx, composeRepo, "default")
	if err != nil || string(data) != "a" {
		t.Fatalf("data %q: %v", data, err)
	}
	if data, err := m.ComposeFile(ctx, composeRepo, "link"); err != nil || data != nil {
		t.Fatalf("a symbolic link was read: %q %v", data, err)
	}
	if data, err := m.ComposeFile(ctx, composeRepo, "missing"); err != nil || data != nil {
		t.Fatalf("missing: %q %v", data, err)
	}
}

func TestComposeFilesFallBackToTheImageBranch(t *testing.T) {
	m := mirrorOf(t, func(dir string) {
		commitFiles(t, dir, map[string]string{"README": "upstream"})
		gitIn(t, dir, "checkout", "-q", "-b", "dyna-images")
		commitFiles(t, dir, map[string]string{".dyna/compose/default.yaml": "branch", ".dyna/images/go/Containerfile": "FROM x", ".dyna/images/rust/Containerfile": "FROM y"})
		gitIn(t, dir, "checkout", "-q", "main")
	})
	ctx := context.Background()
	data, err := m.ComposeFile(ctx, composeRepo, "default")
	if err != nil || string(data) != "branch" {
		t.Fatalf("data %q: %v", data, err)
	}
}

// The three levels together, with the repository level read from a real mirror.
func TestARepositoryFileBeatsARoomFileBeatsAGlobalFile(t *testing.T) {
	m := mirrorOf(t, func(dir string) {
		commitFiles(t, dir, map[string]string{".dyna/compose/shared.yaml": "services:\n  agent:\n    environment: {FROM: repo}\n"})
	})
	files := &compose.Files{Layout: m.Layout, Repo: m, Images: &images.Store{Layout: m.Layout}}
	for dir, data := range map[string]map[string]string{
		files.Layout.Compose(""):  {"shared": "global", "top": "global", "room": "global"},
		files.Layout.Compose("r"): {"shared": "room", "room": "room"},
	} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, value := range data {
			body := "services:\n  agent:\n    environment: {FROM: " + value + "}\n"
			if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, want := range map[string]string{"shared": "repo", "room": "room", "top": "global"} {
		_, file, err := files.Start(context.Background(), "r", composeRepo, name)
		if err != nil {
			t.Fatal(err)
		}
		if got := file.Agent.Env[0].Value; got != want {
			t.Errorf("%s came from %s, want %s", name, got, want)
		}
	}
	listed, err := files.List(context.Background(), "r", composeRepo)
	if err != nil || len(listed) != 3 {
		t.Fatalf("%+v %v", listed, err)
	}
}

func TestAnOversizedRepositoryFileIsNotRead(t *testing.T) {
	m := mirrorOf(t, func(dir string) {
		commitFiles(t, dir, map[string]string{".dyna/compose/big.yaml": strings.Repeat("#", compose.MaxFileBytes+1)})
	})
	data, err := m.ComposeFile(context.Background(), composeRepo, "big")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compose.Parse(data); err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("got %v", err)
	}
}

// Migration copies a repository's images, from its image branch too, into the room once, and the room's own images win.
func TestMigrationCopiesARepositorysImagesIntoTheRoom(t *testing.T) {
	m := mirrorOf(t, func(dir string) {
		commitFiles(t, dir, map[string]string{"README": "upstream"})
		gitIn(t, dir, "checkout", "-q", "-b", "dyna-images")
		commitFiles(t, dir, map[string]string{
			".dyna/images/go/Containerfile":   "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n",
			".dyna/images/rust/Containerfile": "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n",
			".dyna/images/rust/box-needs":     "kvm\n",
			".dyna/images/rust/size":          "large\n",
		})
		gitIn(t, dir, "checkout", "-q", "main")
	})
	ctx := context.Background()
	store := &images.Store{Layout: m.Layout}
	path, err := m.Layout.Git(composeRepo)
	if err != nil {
		t.Fatal(err)
	}
	own := packOne(t, "ARG DYNA_BASE\nFROM ${DYNA_BASE}\nRUN echo mine\n")
	if _, err := store.Save("r", "go", own); err != nil {
		t.Fatal(err)
	}
	copied, left, err := store.Migrate(ctx, m.Runner, "r", composeRepo, path)
	if err != nil || !slices.Equal(copied, []string{"rust"}) || len(left) != 1 {
		t.Fatalf("copied %v left %v: %v", copied, left, err)
	}
	_, _, _, needs, err := store.Context("r", "rust")
	if err != nil || len(needs) != 1 || needs[0] != box.NeedKVM {
		t.Fatalf("needs %v: %v", needs, err)
	}
	if size, err := store.Size("r", "rust"); err != nil || size != "large" {
		t.Fatalf("size %q: %v", size, err)
	}
	if again, _, err := store.Migrate(ctx, m.Runner, "r", composeRepo, path); err != nil || len(again) != 0 {
		t.Fatalf("second run copied %v: %v", again, err)
	}
	if _, _, _, _, err := store.Context("r", ""); !errors.Is(err, images.ErrImageChoice) {
		t.Fatalf("several images, no choice: %v", err)
	}
}

func packOne(t *testing.T, containerfile string) []byte {
	t.Helper()
	archive := skills.NewArchive()
	if err := archive.Add("Containerfile", 0o600, []byte(containerfile)); err != nil {
		t.Fatal(err)
	}
	packed, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return packed
}
