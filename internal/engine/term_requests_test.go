package engine

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/questions"
	"dyna-agent/internal/store/storetest"
)

func TestTerminalResultSharesLinesOnlyWhenChosen(t *testing.T) {
	storetest.Isolate(t)
	t.Cleanup(authority.InEngine())
	s := testServices(t, false)
	info, err := s.terminals.OpenRequest("room_test", "", t.TempDir(), []string{"sh", "-c", "echo secret-output; exit 3"}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	for {
		ev, wake, err := s.terminals.Read(info.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Exited {
			break
		}
		<-wake
	}
	kept, err := s.terminalResult(questions.TerminalReport{}, info.ID, false)
	if err != nil || kept.SharedLines != "" || kept.ExitCode == nil || *kept.ExitCode != 3 {
		t.Fatalf("the exit code must be stored and the lines kept private: %+v, %v", kept, err)
	}
	shared, err := s.terminalResult(questions.TerminalReport{}, info.ID, true)
	if err != nil || !strings.Contains(shared.SharedLines, "secret-output") {
		t.Fatalf("shared lines missing: %+v, %v", shared, err)
	}
}
