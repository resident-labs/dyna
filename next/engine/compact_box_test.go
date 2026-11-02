//go:build linux

package engine

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
)

// One session in a real box on real Podman with the real box agent and the Pi the image ships, against the fake upstream: its
// quiet stop asks over the real channel, Pi measures and compacts, and the compaction reaches the transcript before the stop.
//
// DYNA_BOX_TEST_IMAGE names a local dyna-base image. DYNA_BOX_TEST_BINARY names a static dyna build.
func TestCompactionBeforeAStopInARealBox(t *testing.T) {
	image, binary := os.Getenv("DYNA_BOX_TEST_IMAGE"), os.Getenv("DYNA_BOX_TEST_BINARY")
	if image == "" || binary == "" {
		t.Skip("DYNA_BOX_TEST_IMAGE and DYNA_BOX_TEST_BINARY name a dyna-base image and a dyna build; this test runs a real box")
	}
	s, room, session, base := setup(t)
	s.profiles = boxProfiles{}
	stops := &dormantHost{}
	s.boxes = stops
	var clock atomic.Int64
	clock.Store(time.Unix(1000, 0).UnixMilli())
	s.now = func() time.Time { return time.UnixMilli(clock.Load()) }
	resetLifecycle(s)
	ctx, cancel := context.WithTimeout(base, 6*time.Minute)
	defer cancel()
	data, err := os.MkdirTemp("", "dyna-compact-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(data) })
	layout := box.Layout{Data: filepath.Join(data, "data"), HostData: filepath.Join(data, "host")}
	s.SetLayout(layout)
	record, err := s.store.Session(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	id := record.Box

	provider := &compactProvider{tokens: 150000}
	server := api.New(boxService{Service: s}, func(api.Caller) http.Handler { return http.HandlerFunc(provider.serve) })
	runtimeDir := filepath.Join(data, "rt")
	sockets := api.NewSockets(server, runtimeDir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
	if err := sockets.AddBox(id, session, room); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.HostSocketFolder(id), 0755); err != nil {
		t.Fatal(err)
	}
	slowRelay(t, layout.HostSocket(id), filepath.Join(runtimeDir, "boxes", string(id)+".sock"), 256<<20)
	bin := filepath.Join(layout.HostBin(), "current")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", binary, filepath.Join(bin, "dyna")).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	host, err := podman.New("local", runtimeDir, "h", localRunner{})
	if err != nil {
		t.Fatal(err)
	}
	slice := fmt.Sprintf("dyna-compact-test-%d.slice", time.Now().UnixNano())
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("podman", "logs", "--tail", "40", string(id)).CombinedOutput()
			t.Logf("box log:\n%s", out)
		}
		_ = exec.Command("podman", "rm", "-f", "-t", "0", string(id)).Run()
		_ = exec.Command("podman", "volume", "rm", "-f", "dyna-box-"+string(id)).Run()
		_ = host.RemoveSlice(context.Background(), slice)
	})
	if err := host.EnsureSlice(ctx, box.Slice{Name: slice, CPUs: 2, MemHigh: 2 << 30, MemMax: 3 << 30, Pids: 1024}); err != nil {
		t.Fatal(err)
	}
	if err := host.CreateContainer(ctx, box.ContainerSpec{Box: id, Image: image, Slice: slice, Labels: []box.Label{{Key: "dyna.layout", Value: box.ContainerLayout}}, Mounts: []box.Mount{
		{HostPath: layout.HostSocketFolder(id), BoxPath: "/run/dyna/engine", ReadOnly: true},
		{HostPath: layout.HostBin(), BoxPath: "/opt/dyna/bin", ReadOnly: true},
		{HostPath: filepath.Dir(layout.HostInstructions("")), BoxPath: "/run/dyna/instructions/global", ReadOnly: true, MakeDir: true},
		{HostPath: filepath.Dir(layout.HostInstructions(room)), BoxPath: "/run/dyna/instructions/room", ReadOnly: true, MakeDir: true},
	}}); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	post := func(text string) {
		t.Helper()
		if _, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: session}, Text: text}); problem != nil {
			t.Fatal(problem)
		}
	}
	waitTurns := func(want int) {
		t.Helper()
		for {
			listed, problem := s.EventsList(ctx, owner, contract.EventsListRequest{Room: room, Types: []contract.EventType{"turn.ended"}, Limit: 1000})
			if problem != nil {
				t.Fatal(problem)
			}
			ended := 0
			for _, event := range listed.Events {
				if body := event.Body.(contract.TurnEnded); body.Session == session {
					if body.Outcome != contract.TurnCompleted {
						t.Fatalf("turn %+v", body)
					}
					ended++
				}
			}
			if ended >= want {
				return
			}
			if ctx.Err() != nil {
				t.Fatalf("waited for %d turns, have %d", want, ended)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	post("the first thing")
	if err := host.StartContainer(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitTurns(1)
	post("the second thing")
	waitTurns(2)

	clock.Add((11 * time.Minute).Milliseconds())
	for stops.stops == 0 {
		if ctx.Err() != nil {
			t.Fatal("the box never stopped")
		}
		passLifecycle(t, s, ctx)
		time.Sleep(100 * time.Millisecond)
	}
	events, err := s.store.Events(ctx, room, 0, 256, []contract.EventType{"session.compacted"})
	if err != nil || len(events) != 1 {
		t.Fatalf("recorded %+v: %v", events, err)
	}
	recorded := events[0].Body.(contract.SessionCompacted)
	if recorded.Error != "" || recorded.TokensBefore <= idleCompactTokens || recorded.TokensAfter >= recorded.TokensBefore {
		t.Fatalf("recorded %+v", recorded)
	}
	read, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: session, Limit: 10})
	if problem != nil {
		t.Fatal(problem)
	}
	found := false
	for _, turn := range read.Turns {
		for _, item := range turn.Items {
			found = found || item.Kind == contract.TranscriptCompaction && item.Tokens == recorded.TokensBefore
		}
	}
	if !found {
		t.Fatalf("the transcript shows no compaction: %+v", read.Turns)
	}
	out, err := exec.Command("podman", "exec", string(id), "bash", "-c", "grep -l '\"type\":\"compaction\"' /box/pi/sessions/*.jsonl").CombinedOutput()
	if err != nil || !strings.Contains(string(out), ".jsonl") {
		t.Fatalf("no compaction in Pi's session file: %v: %s", err, out)
	}
}
