package notices

import (
	"strings"
	"testing"
	"time"
)

func TestPostDedupesByKeyAndDismissRemoves(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	resets := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	first, err := s.Post(Notice{Key: "provider:argon", Kind: KindProvider, Severity: SeverityWarn, Provider: &ProviderFacts{Profile: "argon", ResetsAt: resets}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(Notice{Key: "disk", Kind: KindDisk, Severity: SeverityError, Disk: &DiskFacts{Paths: []DiskPath{{Path: "/tmp", FreeBytes: 1024}}}}); err != nil {
		t.Fatal(err)
	}
	again, err := s.Post(Notice{Key: "provider:argon", Kind: KindProvider, Severity: SeverityWarn, Provider: &ProviderFacts{Profile: "argon", ResetsAt: resets.Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != first.ID {
		t.Fatalf("same key got a new ID: %s != %s", again.ID, first.ID)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].Provider == nil || !list[0].Provider.ResetsAt.Equal(resets.Add(-time.Hour)) || list[0].At.IsZero() {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := s.Dismiss(first.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].Key != "disk" {
		t.Fatalf("after dismiss = %+v", list)
	}
	back, _ := s.Post(Notice{Key: "provider:argon", Kind: "provider", Severity: "warn", Text: "again"})
	if back.ID == first.ID {
		t.Fatal("a dismissed key must come back as a new notice")
	}
}

func TestListEmpty(t *testing.T) {
	list, err := Store{Dir: t.TempDir()}.List()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("list = %#v, %v", list, err)
	}
}

// A notice carries the typed facts of its kind, so the Home card can draw it
// without reading a stored sentence. A missing or mismatched fact is refused.
func TestPostRequiresTheFactsOfItsKind(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	disk := &DiskFacts{Paths: []DiskPath{{Path: "/tmp", FreeBytes: 1024}}}
	provider := &ProviderFacts{Profile: "argon", ResetsAt: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)}
	sync := &SyncFacts{Repo: "/repo", Lane: "origin", Other: "fork"}
	for name, n := range map[string]Notice{
		"no facts":         {Key: "k", Kind: KindDisk, Severity: SeverityWarn},
		"another kind's":   {Key: "k", Kind: KindDisk, Severity: SeverityWarn, Provider: provider},
		"two kinds":        {Key: "k", Kind: KindDisk, Severity: SeverityWarn, Disk: disk, Sync: sync},
		"no paths":         {Key: "k", Kind: KindDisk, Severity: SeverityWarn, Disk: &DiskFacts{}},
		"no profile":       {Key: "k", Kind: KindProvider, Severity: SeverityWarn, Provider: &ProviderFacts{}},
		"no lane":          {Key: "k", Kind: KindSync, Severity: SeverityWarn, Sync: &SyncFacts{Repo: "/repo"}},
		"unknown kind":     {Key: "k", Kind: "hook", Severity: SeverityWarn, Disk: disk},
		"unknown severity": {Key: "k", Kind: KindDisk, Severity: "fatal", Disk: disk},
	} {
		if _, err := s.Post(n); err == nil {
			t.Fatalf("%s was accepted: %+v", name, n)
		}
	}
}

// The facts survive the store, and the one line for logs is built from them.
func TestFactsRoundTripAndBuildTheLine(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	resets := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	for _, n := range []Notice{
		{Key: "disk", Kind: KindDisk, Severity: SeverityError, Disk: &DiskFacts{Paths: []DiskPath{{Path: "/tmp", FreeBytes: 2048}}, FreedBytes: 42, Kept: []string{"dirty: 2"}}},
		{Key: "provider:argon", Kind: KindProvider, Severity: SeverityWarn, Provider: &ProviderFacts{Profile: "argon", ResetsAt: resets}},
		{Key: "sync:x", Kind: KindSync, Severity: SeverityWarn, Sync: &SyncFacts{Repo: "/repo", Lane: "origin", Other: "fork"}},
		{Key: "sync:r", Kind: KindSync, Severity: SeverityWarn, Sync: &SyncFacts{Repo: "/repo", Lane: "fork", Rewritten: true}},
	} {
		if _, err := s.Post(n); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil || len(list) != 4 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if got := list[0]; got.Disk == nil || got.Disk.Paths[0].FreeBytes != 2048 || got.Disk.FreedBytes != 42 || !strings.Contains(got.Line(), "/tmp (2048 bytes free)") {
		t.Fatalf("disk = %+v, line %q", got.Disk, got.Line())
	}
	if got := list[1]; got.Provider == nil || !got.Provider.ResetsAt.Equal(resets) || !strings.Contains(got.Line(), "argon") {
		t.Fatalf("provider = %+v, line %q", got.Provider, got.Line())
	}
	if got := list[2]; got.Sync == nil || got.Sync.Other != "fork" || !strings.Contains(got.Line(), "between origin and fork") {
		t.Fatalf("sync = %+v, line %q", got.Sync, got.Line())
	}
	if got := list[3]; got.Sync == nil || !got.Sync.Rewritten || !strings.Contains(got.Line(), "rewritten") {
		t.Fatalf("rewrite = %+v, line %q", got.Sync, got.Line())
	}
	// The managed tunnel reports a missing connector with typed facts: the
	// binary it tried to run and the PATH it searched.
	if _, err := s.Post(Notice{Key: "tunnel", Kind: KindTunnel, Severity: SeverityError, Tunnel: &TunnelFacts{Binary: "cloudflared", Path: "/usr/local/bin:/usr/bin:/bin"}}); err != nil {
		t.Fatal(err)
	}
	list, err = s.List()
	if err != nil || len(list) != 5 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if got := list[4]; got.Tunnel == nil || got.Tunnel.Binary != "cloudflared" || !strings.Contains(got.Line(), "cloudflared") {
		t.Fatalf("tunnel = %+v, line %q", got.Tunnel, got.Line())
	}
}
