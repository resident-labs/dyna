package dynaext

import (
	"context"
	"encoding/json"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestScriptInterruptGraceAndNextCall(t *testing.T) {
	entered := make(chan struct{}, 1)
	slow := extension.NamedInlineExtension{Name: "slow", Factory: func(api extension.API) error {
		api.RegisterTool(extension.ToolDefinition{Name: "slow", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(ctx context.Context, _ string, _ json.RawMessage, _ extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
			entered <- struct{}{}
			<-ctx.Done()
			return extension.AgentToolResult{}, ctx.Err()
		}})
		return nil
	}}
	code := `text("before"); store("discarded", 42); await tools.slow({}); text("after");`
	args, _ := json.Marshal(map[string]string{"code": code})
	b := newSessionOn(t, "test-model", []extension.InlineExtension{slow}, []pigtest.Block{{Tool: "codemode", Input: string(args)}}, []pigtest.Block{{Tool: "codemode", Input: `{"code":"return load('discarded') ?? 'not saved';"}`}}, []pigtest.Block{{Text: "done"}})
	b.interrupts.grace = time.Millisecond
	go func() {
		<-entered
		b.interrupts.Send(contract.Interrupt{Kind: contract.InterruptParent, Name: "build-lead"})
	}()
	b.turn(t, "run")
	got := b.toolResult(t, 1)
	for _, want := range []string{"Script stopped", "before", "cancelled", "build-lead", "not saved"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Contains(got, `after\n`) {
		t.Fatal("continued after cancellation")
	}
	if got = b.toolResult(t, 2); !strings.Contains(got, "not saved") || !strings.Contains(got, "Script completed") {
		t.Fatalf("next script: %s", got)
	}
}

func TestScriptFinishingInsideGraceReturnsNormally(t *testing.T) {
	var interrupts *Interrupts
	ready := extension.NamedInlineExtension{Name: "ready", Factory: func(api extension.API) error {
		api.RegisterTool(extension.ToolDefinition{Name: "ready", Parameters: json.RawMessage(`{"type":"object","properties":{}}`), Execute: func(context.Context, string, json.RawMessage, extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
			interrupts.Send(contract.Interrupt{Kind: contract.InterruptOwner})
			return extension.AgentToolResult{}, nil
		}})
		return nil
	}}
	b := newSessionOn(t, "test-model", []extension.InlineExtension{ready}, []pigtest.Block{{Tool: "codemode", Input: `{"code":"await tools.ready({}); store('kept', 7); return 'finished';"}`}}, []pigtest.Block{{Tool: "codemode", Input: `{"code":"return load('kept');"}`}}, []pigtest.Block{{Text: "done"}})
	interrupts = b.interrupts
	if interrupts.grace != 5*time.Second {
		t.Fatalf("grace %s", interrupts.grace)
	}
	b.turn(t, "run")
	if got := b.toolResult(t, 1); !strings.Contains(got, "Script completed") || strings.Contains(got, "Script stopped") {
		t.Fatal(got)
	}
	if got := b.toolResult(t, 2); !strings.Contains(got, `7`) {
		t.Fatalf("successful store missing: %s", got)
	}
}

func TestScriptCancellationLeavesNestedBashAndOneCompletion(t *testing.T) {
	entered := make(chan struct{}, 1)
	reports := make(chan contract.ShellJobEndedRequest, 4)
	pipe := filepath.Join(t.TempDir(), "release")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"code": `text("partial"); await tools.bash({command:"echo running; cat ` + pipe + `"}); text("never reached");`})
	b := newSessionShell(t, "test-model", nil, nil, shell.Config{After: func(time.Duration) <-chan time.Time {
		select {
		case entered <- struct{}{}:
		default:
		}
		return make(chan time.Time)
	}, Report: func(r contract.ShellJobEndedRequest) error { reports <- r; return nil }}, []pigtest.Block{{Tool: "codemode", Input: string(args)}}, []pigtest.Block{{Text: "done"}})
	b.interrupts.grace = time.Millisecond
	go func() {
		<-entered
		b.interrupts.Send(contract.Interrupt{Kind: contract.InterruptAncestor, Name: "root"})
	}()
	b.turn(t, "run")
	if got := b.toolResult(t, 1); !strings.Contains(got, "Script stopped") || !strings.Contains(got, "partial") || !strings.Contains(got, "bash: done") {
		t.Fatal(got)
	}
	if len(b.shell.Running()) != 1 {
		t.Fatal("nested bash did not survive")
	}
	f, err := os.OpenFile(pipe, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("released\n")
	f.Close()
	select {
	case r := <-reports:
		if r.Job == "" {
			t.Fatal("missing job")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no completion wake")
	}
	select {
	case r := <-reports:
		t.Fatalf("duplicate completion %+v", r)
	default:
	}
}

func TestLoopingBashScriptStopsInsteadOfWaitingForEveryCommand(t *testing.T) {
	entered := make(chan struct{}, 2)
	reports := make(chan contract.ShellJobEndedRequest, 4)
	pipe := filepath.Join(t.TempDir(), "release")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"code": `for(let n=0;n<10;n++){await tools.bash({command:"cat ` + pipe + `"});text("ran " + n);}`})
	b := newSessionShell(t, "test-model", nil, nil, shell.Config{After: func(time.Duration) <-chan time.Time {
		select {
		case entered <- struct{}{}:
		default:
		}
		return make(chan time.Time)
	}, Report: func(r contract.ShellJobEndedRequest) error { reports <- r; return nil }}, []pigtest.Block{{Tool: "codemode", Input: string(args)}}, []pigtest.Block{{Text: "done"}})
	b.interrupts.grace = 100 * time.Millisecond
	go func() {
		<-entered
		sender := contract.Interrupt{Kind: contract.InterruptParent, Name: "build-lead"}
		b.shell.StepAside(sender)
		b.interrupts.Send(sender)
	}()
	start := time.Now()
	b.turn(t, "run")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("script did not yield: %s", elapsed)
	}
	if got := b.toolResult(t, 1); !strings.Contains(got, "Script stopped") || !strings.Contains(got, "ran 0") {
		t.Fatal(got)
	}
	running := len(b.shell.Running())
	if running < 1 || running > 2 {
		t.Fatalf("started %d commands, expected at most the next one", running)
	}
	f, err := os.OpenFile(pipe, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for range running {
		select {
		case <-reports:
		case <-time.After(5 * time.Second):
			t.Fatal("no completion wake")
		}
	}
}
