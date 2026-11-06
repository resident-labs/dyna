package runtime

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

func TestEngineStartupDiscardsCopiedHostIdentityBeforeProfiles(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	for _, host := range []box.Host{{Name: "old", EngineHost: &yes}, {Name: "new", EngineHost: &no}} {
		if err := s.AddHost(context.Background(), host); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A missing profile stops startup before any driver or host action. The reset must already have happened.
	err = Run(context.Background(), Config{Data: dir, Runtime: t.TempDir(), HostData: t.TempDir(), Profiles: filepath.Join(t.TempDir(), "absent.json")})
	if err == nil {
		t.Fatal("startup unexpectedly passed missing profiles")
	}
	s, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	hosts, err := s.Hosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts {
		if host.EngineHost != nil {
			t.Fatalf("copied identity cached for %s", host.Name)
		}
	}
}

func TestDestinationEngineRestoresExistingFUSEForward(t *testing.T) {
	r := &shareHost{}
	b := shareBoxes(t, r, engineHosts("host"))
	h, err := podman.New("pip@100.106.214.124", b.Runtime, "host", r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.hosts["host"] = h
	if err := b.restoreHost(context.Background(), "host"); err != nil {
		t.Fatal(err)
	}
	forward := b.shareForward()
	if forward.HostPath == forward.EnginePath {
		t.Fatal("share forward loops to itself")
	}
	if r.index("-R "+forward.HostPath+":"+forward.EnginePath+" pip@100.106.214.124") < 0 {
		t.Fatalf("destination engine did not restore the distinct self-SSH share socket: %v", r.commands)
	}
}

func TestDestinationEngineShareSocketRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := &sshForwards{}
	b := &Boxes{Store: restoreStore{hosts: engineHosts("host")}, Layout: box.Layout{Data: t.TempDir(), HostData: t.TempDir()}, Runtime: t.TempDir()}
	h, err := podman.New("pip@100.106.214.124", b.Runtime, "host", r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.hosts = map[contract.HostName]*podman.Host{"host": h}
	forward := b.shareForward()
	defer r.close(forward.HostPath)
	if forward.HostPath == forward.EnginePath {
		t.Fatal("share forward loops to itself")
	}
	server, err := net.Listen("unix", forward.EnginePath)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	go func() {
		conn, err := server.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err == nil {
			_, _ = conn.Write(buf)
		}
	}()
	if err := b.restoreHost(ctx, "host"); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", forward.HostPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("round trip %q: %v", buf, err)
	}
}

func TestFormerEngineRecreateUsesRemoteShareAndKeepsVolume(t *testing.T) {
	no := false
	r := &shareHost{}
	b := shareBoxes(t, r, []box.Host{{Name: "host", EngineHost: &no}})
	b.restored = map[contract.HostName]bool{"host": true}
	if err := b.Recreate(context.Background(), lifecycle.BoxSpec{Box: "b", Session: "s", Room: "room_1", Host: "host", Slice: "dyna-b.slice", Size: contract.BoxSize{CPUs: 1, MemBytes: box.GiB, Pids: 512}}, "image"); err != nil {
		t.Fatal(err)
	}
	if r.index("'/host/share/rooms/room_1/shared:/box/shared'") < 0 {
		t.Fatalf("recreate did not bind remote shared folder: %v", r.commands)
	}
	if r.index("'dyna-box-b:/box'") < 0 {
		t.Fatal("recreate did not reuse named volume")
	}
	if r.index("'volume' 'rm'") >= 0 {
		t.Fatal("recreate removed the named volume")
	}
	if r.index("'/data/rooms/room_1/shared:/box/shared'") >= 0 {
		t.Fatal("recreate retained former engine direct bind")
	}
}

func TestEngineMachineIdentityDoesNotComeFromCopiedData(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(map[bool]string{true: "same machine", false: "different machine"}[same], func(t *testing.T) {
			data := t.TempDir()
			machine := filepath.Join(t.TempDir(), "machine-id")
			id := "0123456789abcdef0123456789abcdef"
			if !same {
				id = strings.Repeat("f", 32)
			}
			if err := os.WriteFile(machine, []byte(id), 0600); err != nil {
				t.Fatal(err)
			}
			b := &Boxes{Layout: box.Layout{Data: data, HostData: data}, machineIDFile: machine}
			h, err := podman.New("pip@remote", t.TempDir(), "host", admissionTransport{}, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			got, err := b.engineHost(context.Background(), h)
			if err != nil || got != same {
				t.Fatalf("same machine = %v, got %v, %v", same, got, err)
			}
		})
	}
}
