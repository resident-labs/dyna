package dynaext

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

func TestJobOutputChecksBelongToTheInputRunNotModelRequests(t *testing.T) {
	pipe := filepath.Join(t.TempDir(), "release")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	var waits atomic.Int32
	reports := make(chan contract.ShellJobEndedRequest, 1)
	script := func(code string) []pigtest.Block {
		args, _ := json.Marshal(map[string]string{"code": code})
		return []pigtest.Block{{Tool: "codemode", Input: string(args)}}
	}
	b := newSessionShell(t, "test-model", nil, nil, shell.Config{After: func(d time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		if d == 30*time.Second {
			waits.Add(1)
			ch <- time.Time{}
		}
		return ch
	}, Report: func(r contract.ShellJobEndedRequest) error { reports <- r; return nil }},
		script(fmt.Sprintf(`const r=await tools.bash({command:%q}); store('job',r.job);`, "cat "+pipe)),
		script(`return await tools.job({action:'output',job:load('job')});`),
		script(`return await tools.job({action:'output',job:load('job')});`),
		[]pigtest.Block{{Text: "waiting"}},
		script(`return await tools.job({action:'output',job:load('job')});`),
		[]pigtest.Block{{Text: "waiting again"}})
	b.turn(t, "run")
	if waits.Load() != 2 {
		t.Fatalf("foreground plus first output: %d waits", waits.Load())
	}
	if got := b.toolResult(t, 3); !strings.Contains(got, "is still running; you already checked it this turn. End your turn: Dyna wakes you when it ends.") {
		t.Fatal(got)
	}
	b.turn(t, "new turn")
	if waits.Load() != 3 {
		t.Fatalf("next turn first output did not wait: %d", waits.Load())
	}
	if err := os.WriteFile(pipe, []byte("complete\n"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-reports:
		if r.End == nil || !strings.Contains(r.Tail, "complete") {
			t.Fatalf("completion: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no completion wake report")
	}
}
