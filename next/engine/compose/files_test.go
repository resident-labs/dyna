package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type fakeRepo struct {
	files     map[string]string
	folders   []string
	missing   bool // no mirror yet
	refreshes int
	onRefresh func()
	err       error
}

func (r *fakeRepo) Cloned(contract.RepoName) bool { return !r.missing }
func (r *fakeRepo) Refresh(context.Context, contract.RepoName) error {
	r.refreshes++
	if r.onRefresh != nil {
		r.onRefresh()
	}
	return r.err
}
func (r *fakeRepo) ComposeNames(context.Context, contract.RepoName) ([]string, error) {
	names := make([]string, 0, len(r.files))
	for name := range r.files {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}
func (r *fakeRepo) ComposeFile(_ context.Context, _ contract.RepoName, name string) ([]byte, error) {
	data, ok := r.files[name]
	if !ok {
		return nil, nil
	}
	return []byte(data), nil
}
func (r *fakeRepo) ImageFolders(context.Context, contract.RepoName) ([]string, error) {
	return r.folders, nil
}

func marked(value string) string {
	return "services:\n  agent:\n    environment: {FROM: " + value + "}\n"
}

func levels(t *testing.T, repo *fakeRepo, global, room map[string]string) *Files {
	t.Helper()
	f := &Files{Layout: box.Layout{Data: t.TempDir()}, Repo: repo}
	for dir, files := range map[string]map[string]string{f.Layout.Compose(""): global, f.Layout.Compose("r"): room} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return f
}

func from(t *testing.T, file *File) string {
	t.Helper()
	if file == nil || len(file.Agent.Env) != 1 {
		t.Fatalf("file %+v", file)
	}
	return file.Agent.Env[0].Value
}

func TestTheMostSpecificLevelWinsWhole(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{files: map[string]string{"all": marked("repo")}}
	f := levels(t, repo,
		map[string]string{"all": marked("global"), "two": marked("global"), "one": marked("global")},
		map[string]string{"all": marked("room"), "two": marked("room")})
	for name, want := range map[string]string{"all": "repo", "two": "room", "one": "global"} {
		got, file, err := f.Start(ctx, "r", "owner/repo", name)
		if err != nil || got != name {
			t.Fatalf("%s: %q %v", name, got, err)
		}
		if value := from(t, file); value != want {
			t.Errorf("%s came from %s, want %s", name, value, want)
		}
		loaded, err := f.Load(ctx, "r", "owner/repo", name)
		if err != nil {
			t.Fatal(err)
		}
		if value := from(t, loaded); value != want {
			t.Errorf("load of %s came from %s, want %s", name, value, want)
		}
	}
	// The winner is used whole: nothing of a lower level's file survives.
	f = levels(t, &fakeRepo{}, map[string]string{"all": sidecar()}, map[string]string{"all": marked("room")})
	_, file, err := f.Start(ctx, "r", "", "all")
	if err != nil || len(file.Sidecars) != 0 {
		t.Fatalf("%+v %v", file, err)
	}
}

func TestARoomsFilesAreItsOwn(t *testing.T) {
	f := levels(t, &fakeRepo{}, nil, map[string]string{"x": marked("room")})
	if _, _, err := f.Start(context.Background(), "other", "", "x"); err == nil {
		t.Fatal("another room read the file")
	}
}

func TestLeavingTheNameOutUsesDefault(t *testing.T) {
	ctx := context.Background()
	f := levels(t, &fakeRepo{}, map[string]string{"default": marked("global")}, nil)
	name, file, err := f.Start(ctx, "r", "", "")
	if err != nil || name != Default {
		t.Fatalf("%q %v", name, err)
	}
	if value := from(t, file); value != "global" {
		t.Fatalf("from %s", value)
	}
}

func TestNoFileAtAnyLevelStartsPlain(t *testing.T) {
	repo := &fakeRepo{}
	f := levels(t, repo, nil, nil)
	name, file, err := f.Start(context.Background(), "r", "owner/repo", "")
	if err != nil || name != "" || file != nil {
		t.Fatalf("%q %+v %v", name, file, err)
	}
	if repo.refreshes != 0 {
		t.Fatalf("a session with no compose file fetched %d times", repo.refreshes)
	}
}

func TestAnUnknownNameIsRefused(t *testing.T) {
	_, _, err := levels(t, &fakeRepo{}, nil, nil).Start(context.Background(), "r", "owner/repo", "nope")
	var refused box.StackError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "no file named nope") {
		t.Fatalf("got %v", err)
	}
}

func TestANameCannotLeaveItsFolder(t *testing.T) {
	f := levels(t, &fakeRepo{}, nil, nil)
	if err := os.WriteFile(filepath.Join(f.Layout.Data, "secret.yaml"), []byte(marked("secret")), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../secret", "a/b", "..", ".", "A", "a.b", "-a"} {
		if _, _, err := f.Start(context.Background(), "r", "", name); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestANamedFileIsWorthAFetch(t *testing.T) {
	repo := &fakeRepo{}
	repo.onRefresh = func() { repo.files = map[string]string{"new": marked("repo")} }
	name, _, err := levels(t, repo, nil, nil).Start(context.Background(), "r", "owner/repo", "new")
	if err != nil || name != "new" || repo.refreshes != 1 {
		t.Fatalf("%q refreshes=%d %v", name, repo.refreshes, err)
	}
}

func TestAFirstSessionFetchesTheMirrorItNeeds(t *testing.T) {
	repo := &fakeRepo{missing: true}
	repo.onRefresh = func() { repo.files, repo.missing = map[string]string{"default": marked("repo")}, false }
	name, _, err := levels(t, repo, nil, nil).Start(context.Background(), "r", "owner/repo", "")
	if err != nil || name != Default || repo.refreshes != 1 {
		t.Fatalf("%q refreshes=%d %v", name, repo.refreshes, err)
	}
}

func TestAFailedFetchOnlyStopsANamedFile(t *testing.T) {
	boom := errors.New("network down")
	repo := &fakeRepo{missing: true, err: boom}
	f := levels(t, repo, nil, nil)
	if name, _, err := f.Start(context.Background(), "r", "owner/repo", ""); err != nil || name != "" {
		t.Fatalf("%q %v", name, err)
	}
	if _, _, err := f.Start(context.Background(), "r", "owner/repo", "x"); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}

func TestAFileChoosesAmongSeveralImages(t *testing.T) {
	ctx := context.Background()
	repo := &fakeRepo{folders: []string{"go", "rust"}, files: map[string]string{
		"rust": "services:\n  agent:\n    build: .dyna/images/rust\n",
		"bare": "services:\n  agent: {}\n",
		"gone": "services:\n  agent:\n    build: .dyna/images/zig\n",
		"side": "services:\n  agent:\n    build: .dyna/images/go\n  db:\n    build: .dyna/images/zig\n",
	}}
	f := levels(t, repo, nil, nil)
	if _, file, err := f.Start(ctx, "r", "owner/repo", "rust"); err != nil || file.Agent.Build != "rust" {
		t.Fatalf("%+v %v", file, err)
	}
	for name, want := range map[string]string{"bare": "several images", "gone": "no image folder zig", "side": "no image folder zig"} {
		if _, _, err := f.Start(ctx, "r", "owner/repo", name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestABuildNeedsARepository(t *testing.T) {
	f := levels(t, &fakeRepo{}, map[string]string{"x": "services:\n  agent:\n    build: .dyna/images/rust\n"}, nil)
	if _, _, err := f.Start(context.Background(), "r", "", "x"); err == nil || !strings.Contains(err.Error(), "needs a repository") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadFailsForAFileThatWentAway(t *testing.T) {
	f := levels(t, &fakeRepo{}, nil, nil)
	if _, err := f.Load(context.Background(), "r", "", "gone"); err == nil || !strings.Contains(err.Error(), "no file named gone") {
		t.Fatalf("got %v", err)
	}
}

func TestListNamesEachWinnerAndWhyItWouldFail(t *testing.T) {
	repo := &fakeRepo{files: map[string]string{"both": marked("repo"), "bad": sidecar("privileged: true")}}
	f := levels(t, repo, map[string]string{"both": marked("global"), "top": marked("global")}, map[string]string{"mine": marked("room"), "skipped": "x"})
	if err := os.WriteFile(filepath.Join(f.Layout.Compose(""), "Not A Name.yaml"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	listed, err := f.List(context.Background(), "r", "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if listed[0].Name != "bad" || !strings.Contains(listed[0].Problem, "privileged is not allowed") {
		t.Fatalf("%+v", listed[0])
	}
	var names []string
	for _, entry := range listed[1:] {
		names = append(names, entry.Name+":"+string(entry.Level))
	}
	if want := []string{"both:repo", "mine:room", "skipped:room", "top:global"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v", names)
	}
	if listed[3].Problem == "" {
		t.Fatalf("a broken file listed clean: %+v", listed[3])
	}
}

func TestLocalFilesOverTheLimitAreRefused(t *testing.T) {
	f := levels(t, &fakeRepo{}, map[string]string{"big": strings.Repeat("#", MaxFileBytes+100)}, nil)
	if _, _, err := f.Start(context.Background(), "r", "", "big"); err == nil || !strings.Contains(err.Error(), "over") {
		t.Fatalf("got %v", err)
	}
}
