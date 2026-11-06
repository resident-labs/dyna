package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/podman"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

// Complete a real activation reply exactly as its caller's idle/deadline expires.
// The adapter parses a full frame; absence of a reply is not treated as success.
type binaryCompletionRunner struct {
	failed    bool
	completed bool
	signal    error
}

func (r *binaryCompletionRunner) Run(ctx context.Context, _ []byte, args ...string) ([]byte, error) {
	for _, arg := range args {
		if arg == "-O" || arg == "-M" {
			return nil, nil
		}
	}
	command := args[len(args)-1]
	body, status := "", 0
	if strings.Contains(command, "dyna-probe") {
		body = strings.Repeat("a", 64) + "\n"
	}
	if strings.Contains(command, "'dyna-link'") {
		<-ctx.Done()
		r.completed, r.signal = true, context.Cause(ctx)
		if r.failed {
			body, status = "binary activation denied\n", 1
		}
	}
	return []byte(fmt.Sprintf("%sDYNA-END %020d %03d\n", body, len(body), status)), nil
}

func TestBinaryCompletedActivationPreservesSuccess(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runner := &binaryCompletionRunner{}
				host, err := podman.New("user@host", t.TempDir(), "h", runner, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				b := &Boxes{Layout: box.Layout{HostData: "/host"}, Version: "v2", hosts: map[contract.HostName]*podman.Host{"h": host}}
				b.binOnce.Do(func() { b.binSum = strings.Repeat("a", 64) })
				if cached {
					b.installed = map[contract.HostName]bool{"h": true}
				}
				entered := time.Now()
				err = b.startStep(context.Background(), lifecycle.BoxSpec{Box: "b", Host: "h"}, contract.StartBinary, func(ctx context.Context) error { return b.installHost(ctx, "h") })
				if !runner.completed || runner.signal == nil || time.Since(entered) != box.StartStepBudget {
					t.Fatalf("activation completion/budget fixture: completed=%t signal=%v elapsed=%s", runner.completed, runner.signal, time.Since(entered))
				}
				if err != nil {
					t.Fatalf("confirmed binary activation must stay successful: cached=%t: %v", cached, err)
				}
			})
		})
	}
}

func TestBinaryActivationFailureRetainsIdleCauseAndBudget(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runner := &binaryCompletionRunner{failed: true}
				host, err := podman.New("user@host", t.TempDir(), "h", runner, t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				b := &Boxes{Layout: box.Layout{HostData: "/host"}, Version: "v2", hosts: map[contract.HostName]*podman.Host{"h": host}}
				b.binOnce.Do(func() { b.binSum = strings.Repeat("a", 64) })
				if cached {
					b.installed = map[contract.HostName]bool{"h": true}
				}
				entered := time.Now()
				err = b.installHost(context.Background(), "h")
				if !runner.completed || time.Since(entered) != box.StartStepBudget {
					t.Fatalf("failed activation changed its budget: completed=%t elapsed=%s", runner.completed, time.Since(entered))
				}
				if !errors.Is(err, podman.ErrTransferIdle) || !strings.Contains(err.Error(), "binary activation denied") {
					t.Fatalf("actual binary failure lost its error or idle cause: cached=%t: %v", cached, err)
				}
				named := b.startStep(context.Background(), lifecycle.BoxSpec{Box: "b", Host: "h"}, contract.StartBinary, func(context.Context) error { return err })
				var timeout box.StepTimeout
				if !errors.As(named, &timeout) || timeout.Host != "h" || timeout.Step != contract.StartBinary {
					t.Fatalf("actual binary idle failure lost host/step timeout: %v", named)
				}
			})
		})
	}
}
