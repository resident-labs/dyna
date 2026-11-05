package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestWorkflowCrewBoxGetsTheGPUItsMetaAsksFor(t *testing.T) {
	w := setupWorkflow(t)
	if err := w.store.AddHost(w.ctx, box.Host{Name: "gpu", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: w.now()}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.SetHostGPUs(w.ctx, "gpu", []contract.GPU{{Index: 0, Name: "RTX", MemBytes: 24 * box.GiB}}); err != nil {
		t.Fatal(err)
	}
	started, _ := w.start(w.owner, "export const meta = { name: 'sweep', profiles: ['fast'], gpu: 6 }\nreturn 1\n")
	if b, err := w.store.Box(w.ctx, started.Box); err != nil || b.GPU != 6 {
		t.Fatalf("crew box GPU %d: %v", b.GPU, err)
	}
	started, _ = w.start(w.owner, "export const meta = { name: 'sweep', profiles: ['fast'], gpu: true }\nreturn 1\n")
	if b, err := w.store.Box(w.ctx, started.Box); err != nil || b.GPU != 1 {
		t.Fatalf("crew box GPU %d for gpu: true: %v", b.GPU, err)
	}
	if _, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: "export const meta = { name: 'sweep', profiles: ['fast'], gpu: 99 }\nreturn 1\n"}); problem == nil || problem.Code != contract.ErrGPUTooLarge {
		t.Fatalf("a crew asking for more than any card has: %v", problem)
	}
}

func TestWorkflowReloadKeepsTheRunsGPU(t *testing.T) {
	w := setupWorkflow(t)
	if err := w.store.AddHost(w.ctx, box.Host{Name: "gpu", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: w.now()}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.SetHostGPUs(w.ctx, "gpu", []contract.GPU{{Index: 0, Name: "RTX", MemBytes: 24 * box.GiB}}); err != nil {
		t.Fatal(err)
	}
	started, _ := w.start(w.owner, "export const meta = { name: 'sweep', profiles: ['fast'], gpu: 2 }\nreturn 1\n")
	_, problem := w.WorkflowReload(w.ctx, w.owner, contract.WorkflowReloadRequest{Run: started.Run, Script: "export const meta = { name: 'sweep', profiles: ['fast'], gpu: 4 }\nreturn 2\n"})
	if problem == nil || !strings.Contains(problem.Message, "meta.gpu") {
		t.Fatalf("a reload that changes gpu: %v", problem)
	}
	if _, problem := w.WorkflowReload(w.ctx, w.owner, contract.WorkflowReloadRequest{Run: started.Run, Script: "export const meta = { name: 'sweep', profiles: ['deep'], gpu: 2 }\nreturn 2\n"}); problem != nil {
		t.Fatalf("a reload that keeps gpu: %v", problem)
	}
}

func TestWorkflowMetaGPUThatWouldOverflowBytesIsRefused(t *testing.T) {
	w := setupWorkflow(t)
	if err := w.store.AddHost(w.ctx, box.Host{Name: "gpu", State: box.HostReady, CPUs: 8, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: w.now()}); err != nil {
		t.Fatal(err)
	}
	if err := w.store.SetHostGPUs(w.ctx, "gpu", []contract.GPU{{Index: 0, Name: "RTX", MemBytes: 24 * box.GiB}}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"17179869184", "9223372036"} {
		if _, problem := w.WorkflowRun(w.ctx, w.owner, contract.WorkflowRunRequest{Room: w.room, Script: "export const meta = { name: 'sweep', profiles: ['fast'], gpu: " + n + " }\nreturn 1\n"}); problem == nil {
			t.Errorf("gpu %s was accepted", n)
		}
	}
}
