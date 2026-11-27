package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

// agyHostFake answers the three seeding scripts of one host: its state, the pack it makes, and the unpack it is handed.
type agyHostFake struct {
	mu       sync.Mutex
	state    string
	pack     string
	failPack bool
	packs    []string // the members the pack script was asked for
	unpacked [][]byte
	replaces []string
	failUnp  bool
}

func (f *agyHostFake) Run(_ context.Context, input []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(body string, status int) ([]byte, error) {
		return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", body, len(body), status)), nil
	}
	switch {
	case strings.Contains(command, "dyna-agy-state"):
		return reply(f.state, 0)
	case strings.Contains(command, "dyna-agy-pack"):
		f.packs = append(f.packs, command[strings.Index(command, "dyna-agy-pack"):])
		if f.failPack {
			return reply(f.pack, 3)
		}
		return reply(f.pack, 0)
	case strings.Contains(command, "dyna-agy-unpack"):
		_, bytesIn, _ := bytes.Cut(input, []byte("\n"))
		f.unpacked = append(f.unpacked, append([]byte(nil), bytesIn...))
		f.replaces = append(f.replaces, command[strings.Index(command, "dyna-agy-unpack"):])
		if f.failUnp {
			return reply("tar: something failed", 2)
		}
		return reply("", 0)
	}
	return nil, errors.New("unexpected command")
}

func seedRig(t *testing.T, source, target *agyHostFake) *Boxes {
	t.Helper()
	b := &Boxes{Layout: box.Layout{HostData: "/data"}, hosts: map[contract.HostName]*podman.Host{}}
	for name, f := range map[contract.HostName]*agyHostFake{engine.AgySource: source, "other": target} {
		h, err := podman.New("user@"+string(name), t.TempDir(), string(name), f, "/data")
		if err != nil {
			t.Fatal(err)
		}
		b.hosts[name] = h
	}
	return b
}

const signedIn = "dyna-agy-binary\ndyna-agy-sum 0123abcd\ndyna-agy-account\ndyna-agy-arch Linux x86_64\n"

// A token file's bytes stand in for the account folder: they may reach the target's unpack and nothing else, and a failure of either
// end names no bytes.
func TestSeedAgyCarriesTheFilesAndOnlyThere(t *testing.T) {
	const sentinel = "SENTINEL-TOKEN-4f9c"
	var logged bytes.Buffer
	log.SetOutput(&logged)
	defer log.SetOutput(nil)
	source := &agyHostFake{state: signedIn, pack: sentinel}
	target := &agyHostFake{state: "dyna-agy-arch Linux x86_64\n"}
	b := seedRig(t, source, target)
	if err := b.SeedAgy(t.Context(), "other", false); err != nil {
		t.Fatal(err)
	}
	if len(target.unpacked) != 1 || string(target.unpacked[0]) != sentinel {
		t.Fatalf("the target was handed %q", target.unpacked)
	}
	if len(source.packs) != 1 || !strings.Contains(source.packs[0], "antigravity") || !strings.Contains(source.packs[0], "accounts/main") {
		t.Fatalf("the source packed %v: a host with neither gets both", source.packs)
	}
	if !strings.Contains(target.replaces[0], "0123abcd") {
		t.Fatalf("the program's checksum did not go to the target: %s", target.replaces[0])
	}
	// The source failing to pack, and the target failing to unpack, each give a fixed text.
	for name, rig := range map[string][2]*agyHostFake{
		"pack":   {{state: signedIn, pack: sentinel, failPack: true}, {state: "dyna-agy-arch Linux x86_64\n"}},
		"unpack": {{state: signedIn, pack: sentinel}, {state: "dyna-agy-arch Linux x86_64\n", failUnp: true}},
	} {
		if err := seedRig(t, rig[0], rig[1]).SeedAgy(t.Context(), "other", false); err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if strings.Contains(logged.String(), sentinel) {
		t.Fatalf("the log holds the bytes: %s", logged.String())
	}
}

func TestSeedAgyLeavesWhatIsThereAndGuardsTheArchitecture(t *testing.T) {
	// A host with both is left alone, with nothing packed.
	source := &agyHostFake{state: signedIn, pack: "x"}
	both := &agyHostFake{state: signedIn}
	if err := seedRig(t, source, both).SeedAgy(t.Context(), "other", false); err != nil || len(source.packs) != 0 || len(both.unpacked) != 0 {
		t.Fatalf("a host that has agy was seeded: %v %v %v", err, source.packs, both.unpacked)
	}
	// With replace, its account folder is replaced and its program is not sent.
	if err := seedRig(t, source, both).SeedAgy(t.Context(), "other", true); err != nil || len(both.unpacked) != 1 || strings.Contains(source.packs[0], " 'antigravity'") {
		t.Fatalf("replace: %v %v", err, source.packs)
	}
	// A host with the program and no account gets the account only.
	account := &agyHostFake{state: "dyna-agy-binary\ndyna-agy-arch Linux aarch64\n"}
	source = &agyHostFake{state: signedIn, pack: "x"}
	if err := seedRig(t, source, account).SeedAgy(t.Context(), "other", false); err != nil || strings.Contains(source.packs[0], " 'antigravity'") {
		t.Fatalf("account only: %v %v", err, source.packs)
	}
	// A host without the program and of another kind gets nothing, and it is not a fault of the source.
	source = &agyHostFake{state: signedIn, pack: "x"}
	arm := &agyHostFake{state: "dyna-agy-arch Linux aarch64\n"}
	if err := seedRig(t, source, arm).SeedAgy(t.Context(), "other", false); !errors.Is(err, ErrAgyArch) || len(source.packs) != 0 || len(arm.unpacked) != 0 {
		t.Fatalf("arch: %v %v", err, source.packs)
	}
	// A source without agy has nothing to copy.
	if err := seedRig(t, &agyHostFake{state: "dyna-agy-arch Linux x86_64\n"}, &agyHostFake{state: "dyna-agy-arch Linux x86_64\n"}).SeedAgy(t.Context(), "other", false); err == nil {
		t.Fatal("seeded from a source with no agy")
	}
}
