package watch

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
				runs := &registeredCIRuns{runs: map[string]*registeredCIRun{}}
				runs.closeAdmission()
				runs.Lock()
				added := runs.add("head", &registeredCIRun{})
				runs.Unlock()
				if added {
					t.Fatal("add admitted a run after admission closed")
				}
			},
		},
		{
			name: "add refuses a key that already runs",
			run: func(t *testing.T) {
				current := &registeredCIRun{}
				runs := &registeredCIRuns{runs: map[string]*registeredCIRun{"head": current}}
				runs.Lock()
				added := runs.add("head", &registeredCIRun{})
				runs.Unlock()
				if added {
					t.Fatal("add replaced a key that already runs")
				}
				if runs.runs["head"] != current {
					t.Fatal("add replaced the stored run")
				}
			},
		},
		{
			name: "finish keeps a different stored run",
			run: func(t *testing.T) {
				stored := &registeredCIRun{}
				runs := &registeredCIRuns{runs: map[string]*registeredCIRun{"head": stored}}
				if runs.finish("head", &registeredCIRun{}) {
					t.Fatal("finish removed a different run")
				}
				if runs.runs["head"] != stored {
					t.Fatal("finish replaced or removed the stored run")
				}
			},
		},
		{
			name: "cancelUnwanted cancels unwanted hosted and disabled node runs",
			run: func(t *testing.T) {
				unwantedCancelled := false
				disabledNodeCancelled := false
				runs := &registeredCIRuns{runs: map[string]*registeredCIRun{
					"unwanted": {node: "worker", cancel: func() { unwantedCancelled = true }},
					"disabled": {node: "retired", cancel: func() { disabledNodeCancelled = true }},
				}}
				runs.Lock()
				runs.cancelUnwanted(map[string]registeredCIJob{}, []localci.Node{{Name: "worker"}})
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
				runs := &registeredCIRuns{runs: map[string]*registeredCIRun{
					"local": {node: "worker", local: true, cancel: func() { cancelled = true }},
				}}
				runs.Lock()
				runs.cancelUnwanted(map[string]registeredCIJob{}, []localci.Node{{Name: "worker"}})
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
