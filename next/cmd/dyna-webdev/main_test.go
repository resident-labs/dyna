package main

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

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
