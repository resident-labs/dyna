package room

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRenderDelivery(t *testing.T) {
	tests := []struct {
		body     contract.EventBody
		want     string
		rejected bool
	}{
		{contract.SessionProfileChanged{Profile: "model"}, "Your model is now model.", false},
		{contract.PostCreated{Text: "hello"}, "hello", false},
		{contract.SessionRestarted{}, "Your box was replaced. Your conversation is restored. Unpushed work in the old volume is gone.", false},
		{contract.SessionRestarted{ImageUpdated: true}, "Your box image was updated. Your files and conversation are kept. Anything installed outside /box is gone.", false},
		{contract.SessionStarted{Goal: "work", Brief: "brief"}, "Goal: work\n\nbrief", false},
		{contract.PrOpened{URL: "url"}, "PR opened: url", false},
		{contract.OpDecided{ID: "op_1", State: contract.OpDenied, Decision: contract.Decision{Reason: "not needed"}}, "Operation op_1: denied. not needed", false},
		{contract.OpRan{ID: "op_2", State: contract.OpFailed, Result: contract.OpResult{Error: "failed", Output: "details"}}, "Operation op_2: failed. failed\ndetails", false},
		{contract.TurnEnded{Final: "done"}, "done", false},
		{contract.TurnEnded{Outcome: contract.TurnFailed, Error: "bad"}, "turn failed: bad", false},
		{contract.ShellJobEnded{Job: "job", ExitCode: 3, Tail: "tail"}, "Background job job exited with code 3. Last output:\ntail", false},
		{contract.HostDiskLow{Host: "h", FreeBytes: 149 << 30, FloorBytes: 100 << 30}, "Host h is low on disk: 149 GiB free, 100 GiB wanted. Close sessions that are done. At the floor, new boxes wait and the oldest stopped boxes close.", false},
		{contract.OpRequested{}, "", true},
	}
	for _, tt := range tests {
		got, err := RenderDelivery(tt.body)
		if got != tt.want || (err != nil) != tt.rejected {
			t.Errorf("%T: got %q, %v; want %q, rejected=%v", tt.body, got, err, tt.want, tt.rejected)
		}
	}
}
