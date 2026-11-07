package dynaext

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"

	"github.com/resident-labs/dyna/box/shell"
	"github.com/resident-labs/dyna/contract"
)

func TestJobResult(t *testing.T) {
	owner := &contract.Interrupt{Kind: contract.InterruptOwner}
	parent := &contract.Interrupt{Kind: contract.InterruptParent, Name: "build-lead"}
	ancestor := &contract.Interrupt{Kind: contract.InterruptAncestor, Name: "root"}
	success, failure := 0, 3
	for _, tt := range []struct {
		name   string
		answer shell.Result
		moved  bool
		want   string
	}{
		{
			name:   "job output interrupted without output",
			answer: shell.Result{Running: true, MessageArrived: true, Interrupt: parent},
			want:   "[job job_test] Your parent build-lead" + steppedNote,
		},
		{
			name:   "job output interrupted by owner with output",
			answer: shell.Result{Output: "new output\n", Running: true, MessageArrived: true, Interrupt: owner},
			want:   "new output\n[job job_test] The owner" + steppedNote,
		},
		{
			name:   "job output interrupted by ancestor",
			answer: shell.Result{Running: true, MessageArrived: true, Interrupt: ancestor},
			want:   "[job job_test] root, above you," + steppedNote,
		},
		{
			name:   "bash interrupted",
			answer: shell.Result{Running: true, MessageArrived: true, Interrupt: parent},
			moved:  true,
			want:   "[job job_test] Your parent build-lead" + steppedNote,
		},
		{
			name:   "bash foreground limit",
			answer: shell.Result{Running: true},
			moved:  true,
			want:   "[job job_test] " + movedNote,
		},
		{
			name:   "ordinary running job without output",
			answer: shell.Result{Running: true},
			want:   "[job job_test is still running. Do not check it again: Dyna wakes you with its result when it ends. Do other work now, or end your turn.]",
		},
		{
			name:   "ordinary running job with output",
			answer: shell.Result{Output: "progress\n", Running: true},
			want:   "progress\n[job job_test is still running. Do not check it again: Dyna wakes you with its result when it ends. Do other work now, or end your turn.]",
		},
		{
			name:   "successful job",
			answer: shell.Result{Output: "done\n", Status: "exit 0", ExitCode: &success},
			want:   "done\n[job job_test: exit 0]",
		},
		{
			name:   "finished bash",
			answer: shell.Result{Status: "exit 3", ExitCode: &failure},
			moved:  true,
			want:   "[job job_test: exit 3]",
		},
		{
			name:   "finished result takes precedence over message",
			answer: shell.Result{Status: "exit 0", ExitCode: &success, MessageArrived: true, Interrupt: owner},
			want:   "[job job_test: exit 0]",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.answer.Job = "job_test"
			result, err := jobResult(tt.answer, tt.moved)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Content) != 1 {
				t.Fatalf("content: %+v", result.Content)
			}
			if got := result.Content[0].(ai.TextContent).Text; got != tt.want {
				t.Errorf("text = %q, want %q", got, tt.want)
			}
			var structured shell.Result
			if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(structured, tt.answer) {
				t.Errorf("structured = %+v, want %+v", structured, tt.answer)
			}
			if result.IsError || result.Terminate {
				t.Fatalf("unexpected error or termination: %+v", result)
			}
		})
	}
}
