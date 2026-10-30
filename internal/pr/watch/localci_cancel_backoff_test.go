package watch

import (
	"context"
	"fmt"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostedCancellationBackoff(t *testing.T) {
	now := time.Unix(1000, 0)
	var reads atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	s := &Service{Now: func() time.Time { return now }, LocalCIRegistry: &hostedCIRegistry{}}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		if reads.Add(1) == 1 {
			close(entered)
			<-release
			return fmt.Errorf("list failed")
		}
		return writeFakeJSON(cmd.Stdout, workflowRunsResponse{})
	}
	job := registeredCIJob{record: Record{Host: "github.com", Repo: "owner/repo"}, head: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	done := make(chan error, 1)
	go func() { done <- s.cancelQueuedHostedCI(context.Background(), job, "repo") }()
	<-entered
	if err := s.cancelQueuedHostedCI(context.Background(), job, "repo"); err != nil {
		t.Fatal(err)
	}
	other := job
	other.head = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := s.cancelQueuedHostedCI(context.Background(), other, "repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.cancelQueuedHostedCI(context.Background(), job, "other-repo"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("expected list failure")
	}
	if err := s.cancelQueuedHostedCI(context.Background(), job, "repo"); err != nil {
		t.Fatal(err)
	}
	if got := reads.Load(); got != 3 {
		t.Fatalf("reads during backoff = %d, want 3", got)
	}
	now = now.Add(hostedCancelInterval)
	if err := s.cancelQueuedHostedCI(context.Background(), job, "repo"); err != nil {
		t.Fatal(err)
	}
	if err := s.cancelQueuedHostedCI(context.Background(), job, "repo"); err != nil {
		t.Fatal(err)
	}
	if got := reads.Load(); got != 4 {
		t.Fatalf("reads after retry = %d, want 4", got)
	}
}
