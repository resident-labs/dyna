package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
)

// agyHostFake answers the seeding scripts of one host: its state over the framed path, and the pack it makes and the unpack it is handed as streams.
type agyHostFake struct {
	mu         sync.Mutex
	state      string
	pack       string
	failPack   bool
	packs      []string // the members the pack script was asked for
	unpacked   [][]byte
	unpackCmds []string
	unpackErr  error // what the unpack ends with
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
	}
	return nil, errors.New("unexpected command")
}

// RunStream answers the pack and unpack scripts as streams: the fake source writes its archive to out, and the fake target reads all
// of its input, with nothing in between that a file could hold.
func (f *agyHostFake) RunStream(_ context.Context, in io.Reader, out io.Writer, args ...string) error {
	command := args[len(args)-1]
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.Contains(command, "dyna-agy-pack"):
		f.packs = append(f.packs, command[strings.Index(command, "dyna-agy-pack"):])
		if _, err := io.WriteString(out, f.pack); err != nil {
			return err
		}
		if f.failPack {
			return errors.New("exit status 3")
		}
		return nil
	case strings.Contains(command, "dyna-agy-unpack"):
		got, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		f.unpacked = append(f.unpacked, got)
		f.unpackCmds = append(f.unpackCmds, command[strings.Index(command, "dyna-agy-unpack"):])
		return f.unpackErr
	}
	return errors.New("unexpected stream command")
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
	defer log.SetOutput(os.Stderr)
	source := &agyHostFake{state: signedIn, pack: sentinel}
	target := &agyHostFake{state: "dyna-agy-arch Linux x86_64\n"}
	b := seedRig(t, source, target)
	if err := b.SeedAgy(t.Context(), "other"); err != nil {
		t.Fatal(err)
	}
	if len(target.unpacked) != 1 || string(target.unpacked[0]) != sentinel {
		t.Fatalf("the target was handed %q", target.unpacked)
	}
	if len(source.packs) != 1 || !strings.Contains(source.packs[0], "antigravity") || !strings.Contains(source.packs[0], "accounts/main") {
		t.Fatalf("the source packed %v: a host with neither gets both", source.packs)
	}
	if !strings.Contains(target.unpackCmds[0], "0123abcd") {
		t.Fatalf("the program's checksum did not go to the target: %s", target.unpackCmds[0])
	}
	// The source failing to pack, and the target failing to unpack, each give a fixed text.
	for name, rig := range map[string][2]*agyHostFake{
		"pack":   {{state: signedIn, pack: sentinel, failPack: true}, {state: "dyna-agy-arch Linux x86_64\n"}},
		"unpack": {{state: signedIn, pack: sentinel}, {state: "dyna-agy-arch Linux x86_64\n", unpackErr: errors.New("exit status 2")}},
	} {
		if err := seedRig(t, rig[0], rig[1]).SeedAgy(t.Context(), "other"); err == nil || strings.Contains(err.Error(), sentinel) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if strings.Contains(logged.String(), sentinel) {
		t.Fatalf("the log holds the bytes: %s", logged.String())
	}
}

func TestSeedAgyRefusesASeededHostAndGuardsTheArchitecture(t *testing.T) {
	// A host that has an account folder is refused with the way to seed it again, and nothing is packed or sent.
	source := &agyHostFake{state: signedIn, pack: "x"}
	both := &agyHostFake{state: signedIn}
	err := seedRig(t, source, both).SeedAgy(t.Context(), "other")
	var seeded SeededError
	if !errors.As(err, &seeded) || len(source.packs) != 0 || len(both.unpacked) != 0 {
		t.Fatalf("a seeded host: %v %v %v", err, source.packs, both.unpacked)
	}
	if !strings.Contains(err.Error(), "/data/agy/accounts/main") || !strings.Contains(err.Error(), "remove") || !strings.Contains(err.Error(), "every agy process") {
		t.Fatalf("the refusal does not say how to seed again: %v", err)
	}
	// A host with the program and no account gets the account only.
	account := &agyHostFake{state: "dyna-agy-binary\ndyna-agy-arch Linux aarch64\n"}
	source = &agyHostFake{state: signedIn, pack: "x"}
	if err := seedRig(t, source, account).SeedAgy(t.Context(), "other"); err != nil || strings.Contains(source.packs[0], " 'antigravity'") {
		t.Fatalf("account only: %v %v", err, source.packs)
	}
	// A host without the program and of another kind gets nothing, and it is not a fault of the source.
	source = &agyHostFake{state: signedIn, pack: "x"}
	arm := &agyHostFake{state: "dyna-agy-arch Linux aarch64\n"}
	if err := seedRig(t, source, arm).SeedAgy(t.Context(), "other"); !errors.Is(err, ErrAgyArch) || len(source.packs) != 0 || len(arm.unpacked) != 0 {
		t.Fatalf("arch: %v %v", err, source.packs)
	}
	// A host with the program and no account gets the account only, and the program is not sent.
	if err := seedRig(t, &agyHostFake{state: "dyna-agy-arch Linux x86_64\n"}, &agyHostFake{state: "dyna-agy-arch Linux x86_64\n"}).SeedAgy(t.Context(), "other"); err == nil {
		t.Fatal("seeded from a source with no agy")
	}
}

// A pack that fails midway ends the stream with an error, so the target never sees a clean end of a cut archive and installs nothing.
func TestSeedAgyAFailedPackIsNeverACleanEndForTheTarget(t *testing.T) {
	source := &agyHostFake{state: signedIn, pack: "PARTIAL", failPack: true}
	target := &agyHostFake{state: "dyna-agy-arch Linux x86_64\n"}
	if err := seedRig(t, source, target).SeedAgy(t.Context(), "other"); err == nil {
		t.Fatal("a failed pack was a good seed")
	}
	if len(target.unpacked) != 0 {
		t.Fatalf("the target took a cut archive: %q", target.unpacked)
	}
}

// A seed that loses to another one ends with the seeded answer, not with the broken pipe its source gets when the target stops reading.
func TestSeedAgyALosingSeedSaysSeededNotBrokenPipe(t *testing.T) {
	lost := exec.Command("bash", "-c", "exit 17").Run() // a real exit status, as ssh hands the remote script's on
	source := &agyHostFake{state: signedIn, pack: "ARCHIVE"}
	target := &agyHostFake{state: "dyna-agy-arch Linux x86_64\n", unpackErr: lost}
	err := seedRig(t, source, target).SeedAgy(t.Context(), "other")
	if !errors.As(err, new(SeededError)) {
		t.Fatalf("err = %v", err)
	}
}

// A program path the target cannot use ends the seed with an actionable text, and not with the generic one.
func TestSeedAgyAnUnusableProgramSaysWhatToRemove(t *testing.T) {
	refused := exec.Command("bash", "-c", "exit 18").Run()
	source := &agyHostFake{state: signedIn, pack: "ARCHIVE"}
	target := &agyHostFake{state: "dyna-agy-arch Linux x86_64\n", unpackErr: refused}
	err := seedRig(t, source, target).SeedAgy(t.Context(), "other")
	if !errors.As(err, new(ProgramError)) || !strings.Contains(err.Error(), "/data/agy/antigravity") || !strings.Contains(err.Error(), "remove it") {
		t.Fatalf("err = %v", err)
	}
}
