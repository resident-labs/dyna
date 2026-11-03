package git

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/images"
)

const (
	recipe      = "ARG DYNA_BASE\nFROM ${DYNA_BASE}\n"
	primaryRepo = contract.RepoName("owner/primary")
	otherRepo   = contract.RepoName("owner/other")
)

type migrateRooms struct {
	repos  []contract.RoomRepo
	byRoom map[contract.RoomID][]contract.RoomRepo // when set, each room has its own
}

func (migrateRooms) OpenRooms(context.Context) ([]contract.RoomID, error) {
	return []contract.RoomID{"r"}, nil
}
func (r migrateRooms) RoomRepos(_ context.Context, room contract.RoomID) ([]contract.RoomRepo, error) {
	if r.byRoom != nil {
		return r.byRoom[room], nil
	}
	return r.repos, nil
}

// failing is a git that fails on one subcommand.
type failing struct{ on string }

func (f failing) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	if slices.Contains(args, f.on) {
		return nil, errors.New("git failed")
	}
	return GitRunner{}.Run(ctx, env, args...)
}

// cloneInto makes a bare mirror of one more repository in the layout of m.
func cloneInto(t *testing.T, m *Mirror, repo contract.RepoName, build func(dir string)) {
	t.Helper()
	source := t.TempDir()
	gitIn(t, source, "init", "-q", "-b", "main")
	build(source)
	path, err := m.Layout.Git(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, source, "clone", "-q", "--bare", source, path)
}

func migrator(m *Mirror, runner images.Runner, repos ...contract.RoomRepo) (*images.Migrator, *images.Store) {
	store := &images.Store{Layout: m.Layout}
	return &images.Migrator{Store: store, Rooms: migrateRooms{repos: repos}, Source: images.Source{
		Cloned: m.Cloned, Refresh: func(context.Context, contract.RepoName) error { return nil }, Dir: m.Layout.Git, Runner: runner,
	}}, store
}

func imageFiles(files map[string]string) map[string]string {
	out := make(map[string]string)
	for name, data := range files {
		out[".dyna/images/"+name] = data
	}
	return out
}

func mirrorWith(t *testing.T, repo contract.RepoName, files map[string]string) *Mirror {
	t.Helper()
	m := &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Runner: GitRunner{}}
	cloneInto(t, m, repo, func(dir string) { commitFiles(t, dir, files) })
	return m
}

func TestMigrationReadsTheImageBranchAndCarriesSizeAndNeeds(t *testing.T) {
	m := &Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Runner: GitRunner{}}
	cloneInto(t, m, primaryRepo, func(dir string) {
		commitFiles(t, dir, map[string]string{"README": "upstream"})
		gitIn(t, dir, "checkout", "-q", "-b", "dyna-images")
		commitFiles(t, dir, imageFiles(map[string]string{"horizon/Containerfile": recipe, "horizon/size": "large\n", "horizon/box-needs": "kvm\n"}))
		gitIn(t, dir, "checkout", "-q", "main")
	})
	mig, store := migrator(m, GitRunner{}, contract.RoomRepo{Repo: primaryRepo, Default: true})
	mig.Room(context.Background(), "r", false)
	_, _, _, needs, err := store.Context("r", "")
	if err != nil || len(needs) != 1 || needs[0] != box.NeedKVM {
		t.Fatalf("needs %v: %v", needs, err)
	}
	if size, err := store.Size("r", ""); err != nil || size != "large" {
		t.Fatalf("size %q: %v", size, err)
	}
}

func mustGit(t *testing.T, m *Mirror, repo contract.RepoName) string {
	t.Helper()
	path, err := m.Layout.Git(repo)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigrationRunsOnceAndNeverOverwritesARoomImage(t *testing.T) {
	m := mirrorWith(t, primaryRepo, imageFiles(map[string]string{"go/Containerfile": recipe + "RUN echo legacy\n", "rust/Containerfile": recipe}))
	mig, store := migrator(m, GitRunner{}, contract.RoomRepo{Repo: primaryRepo, Default: true})
	own := packOne(t, recipe+"RUN echo mine\n")
	mine, err := store.Save("r", "go", own)
	if err != nil {
		t.Fatal(err)
	}
	mig.Room(context.Background(), "r", false)
	held, err := store.List("r")
	if err != nil || len(held) != 2 {
		t.Fatalf("%v: %v", held, err)
	}
	for _, image := range held {
		if image.Name == "go" && image.ID != mine.ID {
			t.Fatal("the room's own image was overwritten")
		}
	}
	if err := store.Remove("r", "rust"); err != nil {
		t.Fatal(err)
	}
	mig.Room(context.Background(), "r", false)
	if held, _ := store.List("r"); len(held) != 1 {
		t.Fatalf("a second run copied again: %v", held)
	}
}

func TestOnlyThePrimaryRepositorysImagesMoveWhateverTheOrder(t *testing.T) {
	m := mirrorWith(t, primaryRepo, imageFiles(map[string]string{"horizon/Containerfile": recipe + "RUN echo primary\n"}))
	cloneInto(t, m, otherRepo, func(dir string) {
		commitFiles(t, dir, imageFiles(map[string]string{"horizon/Containerfile": recipe + "RUN echo other\n", "extra/Containerfile": recipe}))
	})
	for name, repos := range map[string][]contract.RoomRepo{
		"primary last":  {{Repo: otherRepo}, {Repo: primaryRepo, Default: true}},
		"primary first": {{Repo: primaryRepo, Default: true}, {Repo: otherRepo}},
	} {
		t.Run(name, func(t *testing.T) {
			mig, store := migrator(m, GitRunner{}, repos...)
			mig.Room(context.Background(), "r", false)
			held, err := store.List("r")
			if err != nil || len(held) != 1 || held[0].Name != "horizon" {
				t.Fatalf("%v: %v", held, err)
			}
			_, _, data, _, err := store.Context("r", "horizon")
			if err != nil || !strings.Contains(string(data), "echo primary") || strings.Contains(string(data), "echo other") {
				t.Fatalf("the other repository's image won: %v", err)
			}
		})
	}
}

func TestAMissingMirrorIsSkippedAndNotRecorded(t *testing.T) {
	m := mirrorWith(t, otherRepo, imageFiles(map[string]string{"x/Containerfile": recipe}))
	mig, store := migrator(m, GitRunner{}, contract.RoomRepo{Repo: primaryRepo, Default: true}, contract.RoomRepo{Repo: otherRepo})
	mig.Room(context.Background(), "r", true)
	if done, _ := store.Migrated("r"); len(done) != 0 {
		t.Fatalf("recorded %v", done)
	}
	if held, _ := store.List("r"); len(held) != 0 {
		t.Fatalf("a non-primary repository's image moved: %v", held)
	}
	cloneInto(t, m, primaryRepo, func(dir string) { commitFiles(t, dir, imageFiles(map[string]string{"late/Containerfile": recipe})) })
	mig.Room(context.Background(), "r", false)
	if held, _ := store.List("r"); len(held) != 1 || held[0].Name != "late" {
		t.Fatalf("the mirror that came late was not migrated: %v", held)
	}
}

func TestAFailedReadLeavesTheRepositoryUnrecorded(t *testing.T) {
	m := mirrorWith(t, primaryRepo, imageFiles(map[string]string{"x/Containerfile": recipe}))
	mig, store := migrator(m, failing{on: "archive"}, contract.RoomRepo{Repo: primaryRepo, Default: true})
	now := time.Now()
	mig.Now = func() time.Time { return now }
	mig.Room(context.Background(), "r", false)
	if done, _ := store.Migrated("r"); len(done) != 0 {
		t.Fatalf("recorded %v", done)
	}
	mig.Source.Runner = GitRunner{}
	now = now.Add(10 * time.Minute)
	mig.Room(context.Background(), "r", false)
	if held, _ := store.List("r"); len(held) != 1 {
		t.Fatalf("the retry did not copy: %v", held)
	}
}

func TestMigrationCopiesUncheckedLeavesOversizeAndSkipsTheRunnerName(t *testing.T) {
	m := mirrorWith(t, primaryRepo, imageFiles(map[string]string{
		"nobase/Containerfile":    "FROM debian\n",
		"big/Containerfile":       recipe,
		"big/blob":                strings.Repeat("x", 4<<20),
		"ci-runner/Containerfile": recipe,
	}))
	mig, store := migrator(m, GitRunner{}, contract.RoomRepo{Repo: primaryRepo, Default: true})
	mig.Room(context.Background(), "r", false)
	held, _ := store.List("r")
	if len(held) != 1 || held[0].Name != "nobase" {
		t.Fatalf("held %v", held)
	}
	if done, _ := store.Migrated("r"); !slices.Equal(done, []string{string(primaryRepo)}) {
		t.Fatalf("a final skip was not recorded: %v", done)
	}
}

func TestARepositoryWithoutImagesMigratesNothingAndTouchesNothing(t *testing.T) {
	m := mirrorWith(t, primaryRepo, map[string]string{".dyna/local-ci.yaml": "x"})
	path := mustGit(t, m, primaryRepo)
	before := gitIn(t, path, "rev-parse", "HEAD")
	mig, store := migrator(m, GitRunner{}, contract.RoomRepo{Repo: primaryRepo, Default: true})
	mig.Room(context.Background(), "r", false)
	if held, _ := store.List("r"); len(held) != 0 {
		t.Fatalf("held %v", held)
	}
	if after := gitIn(t, path, "rev-parse", "HEAD"); after != before {
		t.Fatal("the repository changed")
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

func TestAFailingFetchRetriesOncePerIntervalAndHoldsUpNoOtherRoom(t *testing.T) {
	m := mirrorWith(t, otherRepo, imageFiles(map[string]string{"x/Containerfile": recipe}))
	mig, store := migrator(m, GitRunner{})
	mig.Rooms = migrateRooms{byRoom: map[contract.RoomID][]contract.RoomRepo{
		"slow": {{Repo: primaryRepo, Default: true}},
		"fast": {{Repo: otherRepo, Default: true}},
	}}
	now := time.Now()
	mig.Now = func() time.Time { return now }
	entered, release := make(chan struct{}), make(chan struct{})
	fetches := 0
	mig.Source.Refresh = func(context.Context, contract.RepoName) error {
		fetches++
		close(entered)
		<-release
		return errors.New("network down")
	}
	done := make(chan struct{})
	go func() { mig.Room(context.Background(), "slow", true); close(done) }()
	<-entered
	// The first room sits in its fetch; another room's start goes through.
	mig.Room(context.Background(), "fast", true)
	if held, _ := store.List("fast"); len(held) != 1 {
		t.Fatalf("a room waited on another room's fetch: %v", held)
	}
	close(release)
	<-done
	mig.Source.Refresh = func(context.Context, contract.RepoName) error { fetches++; return errors.New("network down") }
	mig.Room(context.Background(), "slow", true)
	if fetches != 1 {
		t.Fatalf("a start inside the interval fetched again: %d", fetches)
	}
	now = now.Add(10 * time.Minute)
	mig.Room(context.Background(), "slow", true)
	if fetches != 2 {
		t.Fatalf("no retry after the interval: %d", fetches)
	}
}

func TestSeveralRepositoriesAndNoPrimaryIsLoggedOnce(t *testing.T) {
	m := mirrorWith(t, primaryRepo, imageFiles(map[string]string{"x/Containerfile": recipe}))
	mig, store := migrator(m, GitRunner{}, contract.RoomRepo{Repo: primaryRepo}, contract.RoomRepo{Repo: otherRepo})
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	mig.Room(context.Background(), "r", false)
	mig.Room(context.Background(), "r", false)
	if strings.Count(out.String(), "none is primary") != 1 {
		t.Fatalf("log %q", out.String())
	}
	if held, _ := store.List("r"); len(held) != 0 {
		t.Fatalf("moved %v", held)
	}
}
