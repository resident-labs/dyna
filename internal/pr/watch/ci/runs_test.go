package ci

import (
	"testing"

	"dyna-agent/internal/localci"
)

func TestRegisteredCIRuns(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "add refuses a closed run set",
			run: func(t *testing.T) {
				runs := &Runs{Runs: map[string]*Run{}}
				runs.CloseAdmission()
				runs.Lock()
				added := runs.Add("head", &Run{})
				runs.Unlock()
				if added {
					t.Fatal("add admitted a run after admission closed")
				}
			},
		},
		{
			name: "add refuses a key that already runs",
			run: func(t *testing.T) {
				current := &Run{}
				runs := &Runs{Runs: map[string]*Run{"head": current}}
				runs.Lock()
				added := runs.Add("head", &Run{})
				runs.Unlock()
				if added {
					t.Fatal("add replaced a key that already runs")
				}
				if runs.Runs["head"] != current {
					t.Fatal("add replaced the stored run")
				}
			},
		},
		{
			name: "finish keeps a different stored run",
			run: func(t *testing.T) {
				stored := &Run{}
				runs := &Runs{Runs: map[string]*Run{"head": stored}}
				if runs.Finish("head", &Run{}) {
					t.Fatal("finish removed a different run")
				}
				if runs.Runs["head"] != stored {
					t.Fatal("finish replaced or removed the stored run")
				}
			},
		},
		{
			name: "cancelUnwanted cancels unwanted hosted and disabled node runs",
			run: func(t *testing.T) {
				unwantedCancelled := false
				disabledNodeCancelled := false
				runs := &Runs{Runs: map[string]*Run{
					"unwanted": {Node: "worker", Cancel: func() { unwantedCancelled = true }},
					"disabled": {Node: "retired", Cancel: func() { disabledNodeCancelled = true }},
				}}
				runs.Lock()
				runs.CancelUnwanted(map[string]struct{}{}, []localci.Node{{Name: "worker"}})
				runs.Unlock()
				if !unwantedCancelled {
					t.Error("cancelUnwanted kept a hosted run that no job wants")
				}
				if !disabledNodeCancelled {
					t.Error("cancelUnwanted kept a run on a node that no longer takes CI")
				}
			},
		},
		{
			name: "cancelUnwanted keeps an unnamed local run",
			run: func(t *testing.T) {
				cancelled := false
				runs := &Runs{Runs: map[string]*Run{
					"local": {Node: "worker", Local: true, Cancel: func() { cancelled = true }},
				}}
				runs.Lock()
				runs.CancelUnwanted(map[string]struct{}{}, []localci.Node{{Name: "worker"}})
				runs.Unlock()
				if cancelled {
					t.Fatal("cancelUnwanted cancelled a local run that no job names")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}
