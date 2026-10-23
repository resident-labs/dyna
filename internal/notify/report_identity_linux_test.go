package notify

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"dyna-agent/internal/subagent"
)

func TestReportPreparationKeepsExactParentIdentity(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reuse=%t", reuse), func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			parent := e.start(rec, "lead", nil)
			child := e.start(rec, "child", &parent)
			payload := strings.Repeat("result ", 100000)
			if _, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) { turn.Result = payload }); err != nil {
				t.Fatal(err)
			}
			dir, err := e.mgr.Store.Dir(child.ID)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "parent-output", "turn-1.txt")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0600); err != nil {
				t.Fatal(err)
			}
			pipe, err := os.OpenFile(path, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer pipe.Close()
			type outcome struct {
				report Result
				err    error
			}
			done := make(chan outcome, 1)
			ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
			defer cancel()
			go func() { res, err := e.svc.CompleteSessionTurn(ctx, child.ID, 1); done <- outcome{res, err} }()
			// The first byte proves report preparation reached its artifact write.
			// The large payload keeps that write blocked until this test drains it.
			if _, err := io.ReadFull(pipe, make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			if _, err := e.mgr.Store.UpdateMeta(parent.ID, func(meta *subagent.Meta) { meta.Closed = time.Now() }); err != nil {
				t.Fatal(err)
			}
			if reuse {
				e.start(rec, "lead", nil)
			}
			// The turn's report is bounded after it is written to its artifact: the
			// spill holds the result itself, without its surrounding space.
			n := len(strings.TrimSpace(payload))
			if _, err := io.CopyN(io.Discard, pipe, int64(n-1)); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-done:
				if out.err != nil || !reflect.DeepEqual(out.report.Message.To, []string{rec.Name}) {
					t.Fatalf("report lost its exact parent: %+v %v", out.report.Message, out.err)
				}
			case <-ctx.Done():
				t.Fatal("report did not finish after preparation")
			}
		})
	}
}
