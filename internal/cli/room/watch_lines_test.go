package room

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

// TestWatchLinesPrintsOneLinePerPostAndASinceLine checks the --lines output:
// one short line per post in stored order, the waking post last — marked !
// when it needs root — an empty kind printed as message, and one since line a
// restart passes to --since.
func TestWatchLinesPrintsOneLinePerPostAndASinceLine(t *testing.T) {
	var out bytes.Buffer
	write := watchWriter(&out, "work", false, true)
	quiet := []room.Message{
		{ID: "msg_1", From: "work:merge-steward", Kind: "merge-decision", To: []string{"work"}, Text: "Keep the queue paused"},
		{ID: "msg_2", From: "work:worker", Kind: "issue", To: []string{"work"}, Text: "Repair\nthe flaky test"},
	}
	for _, msg := range quiet {
		if err := write(msg); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("quiet posts printed lines before a wake:\n%s", out.String())
	}
	wake := room.Message{Wake: true, ID: "msg_3", From: "work:lead", To: []string{"work"}, NeedsRoot: true, Text: "Choose the recovery"}
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	want := "· work:merge-steward [merge-decision] msg_1: Keep the queue paused\n" +
		"· work:worker [issue] msg_2: Repair\n" +
		"! work:lead [message] msg_3: Choose the recovery\n" +
		"-- since msg_3\n"
	if got := out.String(); got != want {
		t.Fatalf("lines output =\n%s\nwant\n%s", got, want)
	}
}

// A waking post without a needs-root mark prints >, and its first text line
// is cut to 200 runes so a notification stays short.
func TestWatchLinesCutLongTextToOneLine(t *testing.T) {
	var out bytes.Buffer
	write := watchWriter(&out, "work", false, true)
	long := strings.Repeat("界", 250)
	if err := write(room.Message{Wake: true, ID: "msg_x", From: "work:worker", Kind: "alert", To: []string{"work"}, Text: long + "\nsecond line"}); err != nil {
		t.Fatal(err)
	}
	want := "> work:worker [alert] msg_x: " + strings.Repeat("界", 200) + "…\n" +
		"-- since msg_x\n"
	if got := out.String(); got != want {
		t.Fatalf("lines output =\n%s\nwant\n%s", got, want)
	}
}

// The owner's post to an agent is quiet context, not a monitor wake; the next
// waking post delivers it. Both roots classify it the same way.
func TestWatchLinesHoldsOwnerDMsUntilAWake(t *testing.T) {
	var out bytes.Buffer
	write := watchWriter(&out, "work", false, true)
	dm := room.Message{Wake: true, ID: "msg_dm", From: "work", Human: true, Kind: "message", To: []string{"work:worker"}, Via: []string{"@worker"}, Text: "fix the flaky test"}
	if err := write(dm); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("an owner DM woke the monitor:\n%s", out.String())
	}
	alert := room.Message{Wake: true, ID: "msg_alert", From: "work:worker", Kind: "alert", To: []string{"work"}, Text: "Main is red"}
	if err := write(alert); err != nil {
		t.Fatal(err)
	}
	want := "· work [message] msg_dm: fix the flaky test\n" +
		"> work:worker [alert] msg_alert: Main is red\n" +
		"-- since msg_alert\n"
	if got := out.String(); got != want {
		t.Fatalf("lines output =\n%s\nwant\n%s", got, want)
	}
}

// --lines is the batched feed as text; --all is a raw JSON stream. The two
// output modes cannot combine.
func TestWatchRejectsLinesWithAll(t *testing.T) {
	storetest.Isolate(t)
	workRoom(t)
	cmd := watchCmd()
	if _, err := execCmd(t, cmd, "work", "--lines", "--all"); err == nil || !strings.Contains(err.Error(), "choose one") {
		t.Fatalf("--lines --all accepted: %v", err)
	}
}
