package room

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestBoxUpdateNoticeNamesWhatOutlastsTheTurn(t *testing.T) {
	turn := contract.Responsibility{Kind: contract.ResponsibilityTurn, ID: "turn_a"}
	job := contract.Responsibility{Kind: contract.ResponsibilityShellJob, ID: "job_7f3b"}
	other := contract.Responsibility{Kind: contract.ResponsibilityShellJob, ID: "job_9c1d"}
	unnamed := contract.Responsibility{Kind: contract.ResponsibilityShellJob}
	terminal := contract.Responsibility{Kind: contract.ResponsibilityTerminal}
	crew := contract.Responsibility{Kind: contract.ResponsibilityWorkflow, ID: "wf_a"}
	goal := contract.Responsibility{Kind: contract.ResponsibilityGoal, ID: "g1"}
	const before, after = "An update is ready for your box. It applies when ", ". Your files and conversation are kept; finished jobs are not, so a job's output after that is only what its end notice shows."
	for _, tc := range []struct {
		held []contract.Responsibility
		want string
	}{
		{nil, ""},
		{[]contract.Responsibility{goal}, ""},
		{[]contract.Responsibility{turn}, ""},
		{[]contract.Responsibility{turn, crew}, ""},
		{[]contract.Responsibility{job}, before + "job_7f3b ends" + after},
		{[]contract.Responsibility{unnamed}, before + "a shell job ends" + after},
		{[]contract.Responsibility{terminal}, before + "your open terminal ends" + after},
		{[]contract.Responsibility{turn, job}, before + "job_7f3b and your open turn end" + after},
		{[]contract.Responsibility{job, other, goal}, before + "job_7f3b and job_9c1d end" + after},
		{[]contract.Responsibility{turn, job, other, terminal}, before + "job_7f3b, job_9c1d, your open terminal and your open turn end" + after},
	} {
		if got := BoxUpdateNotice(tc.held); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.held, got, tc.want)
		}
	}
}
