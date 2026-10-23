package machines

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// probeText is what one probe prints on a Linux machine.
const probeText = `os=Linux
arch=x86_64
cpus=16
memtotal=66897960960
memavailable=44691999744
load1=0.42
load5=0.38
load15=0.35
idle=4200
ticks=6000
gpu=yes
has_docker=yes
has_podman=no
has_labwc=yes
has_Xwayland=yes
has_wayvnc=yes
dyna=dyna 0.9.412`

func TestParseProbe(t *testing.T) {
	caps, load, err := parseProbe(probeText)
	if err != nil {
		t.Fatal(err)
	}
	want := Capabilities{
		OS: "Linux", Arch: "x86_64", CPUs: 16,
		MemTotal: 66897960960, MemAvailable: 44691999744,
		GPU:         true,
		Containers:  Containers{Docker: true},
		Computers:   Computers{Labwc: true, Xwayland: true, Stream: true},
		DynaVersion: "dyna 0.9.412",
	}
	if !reflect.DeepEqual(caps, want) {
		t.Fatalf("capabilities = %#v, want %#v", caps, want)
	}
	if load.CPUs != 16 || load.MemTotal != 66897960960 || load.MemAvailable != 44691999744 {
		t.Fatalf("load = %#v", load)
	}
	if load.LoadAvg != [3]float64{0.42, 0.38, 0.35} {
		t.Fatalf("load average = %v", load.LoadAvg)
	}
	// 1800 of 6000 ticks were busy.
	if load.CPUPercent == nil || *load.CPUPercent != 30 {
		t.Fatalf("CPU percent = %v", load.CPUPercent)
	}
	if !caps.Computers.Ready() {
		t.Fatal("a machine with labwc and Xwayland cannot run a computer")
	}
	// Streaming is a separate capability: a machine without wayvnc still runs
	// computers, and this one has it.
	if !caps.Computers.Stream {
		t.Fatal("a machine with wayvnc does not report streaming")
	}
}

func TestFailedProbeKeepsCapabilities(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	hermes, err := s.Save(ctx, Machine{Name: "hermeslab", Roles: []Role{RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// The probe reads a remote machine over SSH, with no credential and no
	// shell syntax of its own.
	var ran []string
	prints := func(out string) Command {
		return func(_ context.Context, _ string, _ io.Reader, w, _ io.Writer, name string, args ...string) error {
			ran = append(ran, strings.Join(append([]string{name}, args...), " "))
			_, err := io.WriteString(w, out)
			return err
		}
	}
	fails := func(err error) Command {
		return func(context.Context, string, io.Reader, io.Writer, io.Writer, string, ...string) error {
			return err
		}
	}

	stored, err := Prober{Store: s, Command: prints(probeText)}.ProbeAndSave(ctx, hermes)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ssh -o BatchMode=yes -o ConnectTimeout=10 pip@hermeslab sh -s"; ran[0] != want {
		t.Fatalf("probe command = %q, want %q", ran[0], want)
	}
	if stored.Status() != HealthOnline || stored.Capabilities.CPUs != 16 {
		t.Fatalf("machine after a good probe = %#v", stored)
	}
	if stored.LastSeen.IsZero() || stored.ProbeError != "" {
		t.Fatalf("machine after a good probe = %#v", stored)
	}
	seen := stored.LastSeen

	// A probe that cannot reach the machine keeps what the last good probe
	// read, so the owner still sees what the machine has.
	down := Prober{Store: s, Command: fails(errors.New("ssh: connect to host pip@hermeslab port 22: Connection refused"))}
	stored, err = down.ProbeAndSave(ctx, stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status() != HealthOffline {
		t.Fatalf("status after a failed probe = %s", stored.Status())
	}
	if !strings.Contains(stored.ProbeError, "Connection refused") {
		t.Fatalf("probe error = %q", stored.ProbeError)
	}
	if stored.Capabilities.CPUs != 16 || stored.Load.CPUs != 16 {
		t.Fatalf("failed probe lost the capabilities: %#v", stored.Capabilities)
	}
	if !stored.LastSeen.Equal(seen) {
		t.Fatalf("failed probe moved last seen from %s to %s", seen, stored.LastSeen)
	}

	// This machine is read with a local shell, and a disabled machine is not
	// probed at all.
	local, err := s.GetLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Prober{Store: s, Command: prints(probeText)}).ProbeAndSave(ctx, local); err != nil {
		t.Fatal(err)
	}
	if want := "sh -s"; ran[len(ran)-1] != want {
		t.Fatalf("local probe command = %q, want %q", ran[len(ran)-1], want)
	}
	if err := s.SetEnabled(ctx, hermes.Name, false); err != nil {
		t.Fatal(err)
	}
	off, err := s.Get(ctx, hermes.Name)
	if err != nil {
		t.Fatal(err)
	}
	if off.Status() != HealthDisabled {
		t.Fatalf("disabled machine shows %s", off.Status())
	}
	if _, err := (Prober{Store: s, Command: prints(probeText)}).ProbeAndSave(ctx, off); !errors.Is(err, ErrDisabled) {
		t.Fatalf("probed a disabled machine: %v", err)
	}
}
