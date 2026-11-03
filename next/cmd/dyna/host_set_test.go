package main

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestHostAddSwitchesDefaultToAgentsOnCIUnset(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "host", "add", "h", "--ssh", "u@h", "--owner-only-admin")
	if f.host.Agents == nil || !*f.host.Agents || f.host.CI != nil {
		t.Fatalf("defaults: %+v", f.host)
	}
	runCLI(t, f, "host", "add", "pve", "--ssh", "b@pve", "--owner-only-admin", "--agents", "off", "--ci", "on")
	if f.host.Agents == nil || *f.host.Agents || f.host.CI == nil || !*f.host.CI {
		t.Fatalf("a CI host: %+v", f.host)
	}
	if _, err := execCLI(&fakeClient{}, "host", "add", "h", "--ssh", "u@h", "--owner-only-admin", "--ci", "yes"); err == nil {
		t.Fatal("--ci yes was accepted")
	}
}

func TestHostSetSendsOnlyTheSwitchesGiven(t *testing.T) {
	f := &fakeClient{}
	runCLI(t, f, "host", "set", "pve", "--agents", "off")
	if f.hostSet == nil || f.hostSet.Host != "pve" || f.hostSet.Agents == nil || *f.hostSet.Agents || f.hostSet.CI != nil {
		t.Fatalf("agents alone: %+v", f.hostSet)
	}
	runCLI(t, f, "host", "set", "pve", "--ci", "on")
	if f.hostSet.Agents != nil || f.hostSet.CI == nil || !*f.hostSet.CI {
		t.Fatalf("ci alone: %+v", f.hostSet)
	}
	runCLI(t, f, "host", "set", "pve", "--agents", "off", "--ci", "off")
	if f.hostSet.Agents == nil || *f.hostSet.Agents || f.hostSet.CI == nil || *f.hostSet.CI {
		t.Fatalf("both off: %+v", f.hostSet)
	}
	for _, args := range [][]string{{"host", "set", "pve"}, {"host", "set", "pve", "--agents", "maybe"}} {
		g := &fakeClient{}
		if _, err := execCLI(g, args...); err == nil || g.hostSet != nil {
			t.Fatalf("%v: err=%v sent=%+v", args, err, g.hostSet)
		}
	}
}

func TestHostListShowsBothSwitches(t *testing.T) {
	f := &fakeClient{hosts: []contract.HostSummary{
		{Name: contract.ControlPlane, DiskBytes: 100 << 30, DiskUsed: 40 << 30},
		{Name: "pve", DiskBytes: 100 << 30, DiskUsed: 10 << 30, CI: true},
		{Name: "worker", DiskBytes: 100 << 30, DiskUsed: 10 << 30, Agents: true},
	}}
	out := runCLI(t, f, "host", "list")
	want := "control-plane\t40 GiB\t100 GiB\tok\n" +
		"pve\t10 GiB\t100 GiB\tok\tagents off\tci on\n" +
		"worker\t10 GiB\t100 GiB\tok\tagents on\tci off\n"
	if out != want {
		t.Fatalf("output=%q", out)
	}
}
