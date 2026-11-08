package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine"
	"github.com/resident-labs/dyna/engine/store"
)

func TestHostListKeepsStoredIdentities(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(strconv.FormatBool(existing), func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if existing {
				for _, name := range []contract.HostName{"gpu-box", "omarchy"} {
					if err := st.AddHost(ctx, box.Host{Name: name, State: box.HostReady, CPUs: 7}); err != nil {
						t.Fatal(err)
					}
				}
			}
			d := devService{Service: engine.New(st, nil, profiles{}, time.Now)}
			list, problem := d.HostList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.HostListRequest{})
			if problem != nil {
				t.Fatal(problem)
			}
			seen := map[contract.HostName]bool{}
			for _, h := range list.Hosts {
				if seen[h.Name] {
					t.Fatalf("duplicate host %q", h.Name)
				}
				seen[h.Name] = true
				if existing && (h.Name == "omarchy" || h.Name == "gpu-box") && h.CPUs != 7 {
					t.Fatalf("stored host overwritten: %+v", h)
				}
			}
			if !seen["omarchy"] || !seen["gpu-box"] {
				t.Fatal("missing fixture hosts")
			}
		})
	}
}

func TestRenameReachesListAndUsage(t *testing.T) {
	ctx := context.Background()
	d := devService{renames: &devRenames{labels: map[contract.AccountID]string{}}}
	if _, problem := d.AccountsRename(ctx, contract.Principal{}, contract.AccountsRenameRequest{AccountID: "studio", Label: " Work "}); problem != nil {
		t.Fatal(problem)
	}
	list, _ := d.AccountsList(ctx, contract.Principal{}, contract.AccountsListRequest{})
	listed := map[contract.AccountID]string{}
	for _, account := range list.Accounts {
		listed[account.ID] = account.Label
	}
	quota, _ := d.AccountsQuota(ctx, contract.Principal{}, contract.AccountsQuotaRequest{})
	for _, usage := range quota.Accounts {
		if usage.Label != listed[usage.AccountID] {
			t.Fatalf("%s: usage %q, list %q", usage.AccountID, usage.Label, listed[usage.AccountID])
		}
	}
	if listed["studio"] != "Work" {
		t.Fatalf("list: %q", listed["studio"])
	}
	if _, problem := d.AccountsRename(ctx, contract.Principal{}, contract.AccountsRenameRequest{AccountID: "studio", Label: "a@b"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("invalid: %v", problem)
	}
	if _, problem := d.AccountsRename(ctx, contract.Principal{}, contract.AccountsRenameRequest{AccountID: "missing", Label: "Work"}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("unknown: %v", problem)
	}
}

func TestInspectStoppedBox(t *testing.T) {
	ctx := context.Background()
	b := &boxes{cancel: map[contract.BoxID]context.CancelFunc{"b": func() {}}}
	status, present, err := b.Inspect(ctx, "b")
	if err != nil || !present || !status.Running {
		t.Fatalf("running: status=%+v present=%v err=%v", status, present, err)
	}
	if err := b.Stop(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	status, present, err = b.Inspect(ctx, "b")
	if err != nil || !present || status.Running {
		t.Fatalf("stopped: status=%+v present=%v err=%v", status, present, err)
	}
	_, present, err = b.Inspect(ctx, "missing")
	if err != nil || present {
		t.Fatalf("missing: present=%v err=%v", present, err)
	}
}
