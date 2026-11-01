package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/images"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/compose"
	enginegit "github.com/resident-labs/dyna/engine/git"
	engineimages "github.com/resident-labs/dyna/engine/images"
	"github.com/resident-labs/dyna/engine/store"
)

type aheadStore struct {
	boxStore
	hosts []box.Host
	rooms map[contract.RoomID][]contract.RepoName
}

func (s aheadStore) Hosts(context.Context) ([]box.Host, error) { return s.hosts, nil }
func (aheadStore) PinWorkflowImage(context.Context, contract.SessionID, string) (string, error) {
	return "", nil
}
func (s aheadStore) OpenRooms(context.Context) ([]contract.RoomID, error) {
	var rooms []contract.RoomID
	for room := range s.rooms {
		rooms = append(rooms, room)
	}
	slices.Sort(rooms)
	return rooms, nil
}
func (s aheadStore) RoomRepos(_ context.Context, room contract.RoomID) ([]contract.RoomRepo, error) {
	var repos []contract.RoomRepo
	for _, repo := range s.rooms[room] {
		repos = append(repos, contract.RoomRepo{Repo: repo})
	}
	return repos, nil
}
func (s aheadStore) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Room: "room_1", Repo: "owner/repo"}, nil
}

// aheadHost answers as a host where no image exists yet, and records each command in order.
type aheadHost struct {
	mu       sync.Mutex
	commands []string
}

func (r *aheadHost) Run(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if slices.Contains(args, "-O") || slices.Contains(args, "-M") {
		return nil, nil
	}
	command := args[len(args)-1]
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	body, status := "", 0
	switch {
	case strings.Contains(command, "'image' 'exists'"):
		status = 1
	case strings.Contains(command, "'podman' 'images'"):
		body = "[]"
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", body, len(body), status)), nil
}

func (r *aheadHost) count(part string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, command := range r.commands {
		if strings.Contains(command, part) {
			n++
		}
	}
	return n
}

// aheadGit is a mirror of owner/repo with one toolchain image, where owner/missing has no mirror at all.
type aheadGit struct{}

func (aheadGit) Run(_ context.Context, _ []string, args ...string) ([]byte, error) {
	if strings.Contains(args[1], "missing") {
		return nil, errors.New("not a git repository")
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "ls-tree HEAD .dyna/images"):
		return []byte("040000 tree aaa\t.dyna/images\n"), nil
	case strings.Contains(joined, "ls-tree aaa"):
		return []byte("040000 tree bbb\ttoolchain\n"), nil
	case strings.Contains(joined, "archive"):
		return []byte("context"), nil
	}
	return nil, nil
}

type aheadEvents struct{}

func (aheadEvents) Record(context.Context, contract.RoomID, contract.EventBody) error { return nil }

func newAheadBoxes(t *testing.T, r *aheadHost, hosts []box.Host, rooms map[contract.RoomID][]contract.RepoName) *Boxes {
	t.Helper()
	b := &Boxes{Store: aheadStore{hosts: hosts, rooms: rooms}, PiTarball: []byte("pi"), Mirror: &enginegit.Mirror{Layout: box.Layout{Data: t.TempDir(), HostData: "/host"}, Runner: aheadGit{}}, Images: &engineimages.Builder{Recorder: aheadEvents{}, Now: time.Now}}
	b.hosts = make(map[contract.HostName]*podman.Host)
	for _, host := range hosts {
		h, err := podman.New("user@host", t.TempDir(), string(host.Name), r)
		if err != nil {
			t.Fatal(err)
		}
		b.hosts[host.Name] = h
	}
	return b
}

func TestBuildAheadBuildsBaseAndEachRepositoryImageThenPrunes(t *testing.T) {
	r := &aheadHost{}
	b := newAheadBoxes(t, r, []box.Host{{Name: "one", State: box.HostReady}}, map[contract.RoomID][]contract.RepoName{"room_1": {"owner/repo"}, "room_2": {"owner/repo"}})
	b.BuildAhead(context.Background())
	if r.count("'-t' 'dyna-base:") == 0 || r.count("'-t' 'dyna-img-toolchain:") == 0 {
		t.Fatalf("commands %v", r.commands)
	}
	if r.count("'podman' 'images'") != 1 {
		t.Fatalf("no prune after the builds: %v", r.commands)
	}
	if last := r.commands[len(r.commands)-1]; !strings.Contains(last, "'podman' 'images'") {
		t.Fatalf("prune ran before the builds ended: %s", last)
	}
}

func TestBuildAheadCoversComposeImages(t *testing.T) {
	r := &aheadHost{}
	b := newAheadBoxes(t, r, []box.Host{{Name: "one", State: box.HostReady}}, map[contract.RoomID][]contract.RepoName{"room_1": {"owner/repo"}})
	path, err := b.Mirror.Layout.Git("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	b.Compose = &compose.Files{Layout: b.Mirror.Layout, Repo: b.Mirror}
	dir := b.Compose.Layout.Compose("room_1")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"good":   "services:\n  agent: {}\n  db:\n    image: postgres:16\n",
		"broken": "services:\n  agent: {}\n  db:\n    image: a\n    privileged: true\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b.BuildAhead(context.Background())
	if r.count("podman pull") == 0 || r.count("podman create --replace") == 0 {
		t.Fatalf("the sidecar image was not pulled and warmed: %v", r.commands)
	}
	if last := r.commands[len(r.commands)-1]; !strings.Contains(last, "'podman' 'images'") {
		t.Fatalf("a broken file stopped the pass: %s", last)
	}
}

func TestBuildAheadSkipsHostsThatAreNotReady(t *testing.T) {
	r := &aheadHost{}
	b := newAheadBoxes(t, r, []box.Host{{Name: "down", State: box.HostUnreachable}}, nil)
	b.BuildAhead(context.Background())
	if len(r.commands) != 0 {
		t.Fatalf("commands %v", r.commands)
	}
}

func TestBuildAheadLeavesRoomsTheHostDoesNotServe(t *testing.T) {
	r := &aheadHost{}
	b := newAheadBoxes(t, r, []box.Host{{Name: "one", State: box.HostReady, Rooms: []contract.RoomID{"room_2"}}}, map[contract.RoomID][]contract.RepoName{"room_1": {"owner/repo"}})
	b.BuildAhead(context.Background())
	if r.count("dyna-img-") != 0 {
		t.Fatalf("built another room's image: %v", r.commands)
	}
	if r.count("'-t' 'dyna-base:") == 0 {
		t.Fatalf("no base build: %v", r.commands)
	}
}

func TestBuildAheadCarriesOnPastARepositoryWithoutAMirror(t *testing.T) {
	r := &aheadHost{}
	b := newAheadBoxes(t, r, []box.Host{{Name: "one", State: box.HostReady}}, map[contract.RoomID][]contract.RepoName{"room_1": {"owner/missing"}, "room_2": {"owner/repo"}})
	b.BuildAhead(context.Background())
	if r.count("'-t' 'dyna-img-toolchain:") == 0 || r.count("'podman' 'images'") != 1 {
		t.Fatalf("one bad repository stopped the pass: %v", r.commands)
	}
}

// A new box and a box swapped at wake both call CurrentImage, so it must agree with what was built ahead.
func TestBuildAheadMakesTheTagABoxWillAskFor(t *testing.T) {
	r := &aheadHost{}
	b := newAheadBoxes(t, r, []box.Host{{Name: "one", State: box.HostReady}}, map[contract.RoomID][]contract.RepoName{"room_1": {"owner/repo"}})
	b.BuildAhead(context.Background())
	image, err := b.CurrentImage(context.Background(), "ses_1", "one")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(image.Tag, "dyna-img-toolchain:") || r.count("'-t' '"+image.Tag+"'") == 0 {
		t.Fatalf("box asks for %s, which was not built ahead: %v", image.Tag, r.commands)
	}
	base, err := b.hosts["one"].BuildBase(context.Background(), images.BaseContainerfile, b.PiTarball)
	if err != nil || r.count("'-t' '"+base+"'") == 0 {
		t.Fatalf("base %s: %v", base, err)
	}
}
