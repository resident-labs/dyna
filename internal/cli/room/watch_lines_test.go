package room

import (
	"bytes"
	"fmt"
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
	seat, post, _ := watchFixture(t)
	var out bytes.Buffer
	write := watchWriter(&out, seat, false, true)
	quiet := []room.Message{
		post(room.PostInput{From: "work:lead", Kind: "message", To: []string{"work"}, Text: "Keep the queue paused"}),
		post(room.PostInput{From: "work:worker", Kind: "message", To: []string{"work"}, Text: "Repair\nthe flaky test"}),
	}
	for _, msg := range quiet {
		if err := write(msg); err != nil {
			t.Fatal(err)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("quiet posts printed lines before a wake:\n%s", out.String())
	}
	wake := post(room.PostInput{Wake: true, From: "work:lead", NeedsRoot: true, To: []string{"work"}, Text: "Choose the recovery"})
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("· work:lead [message] %s: Keep the queue paused\n", quiet[0].ID) +
		fmt.Sprintf("· work:worker [message] %s: Repair\n", quiet[1].ID) +
		fmt.Sprintf("! work:lead [message] %s: Choose the recovery\n", wake.ID) +
		fmt.Sprintf("-- since %s\n", wake.ID)
	if got := out.String(); got != want {
		t.Fatalf("lines output =\n%s\nwant\n%s", got, want)
	}
}

// A waking post without a needs-root mark prints >, and its first text line
// is cut to 200 runes so a notification stays short.
func TestWatchLinesCutLongTextToOneLine(t *testing.T) {
	seat, post, _ := watchFixture(t)
	var out bytes.Buffer
	write := watchWriter(&out, seat, false, true)
	long := strings.Repeat("界", 250)
	wake := post(room.PostInput{Wake: true, From: "work:worker", Kind: "service-alert", To: []string{"work"}, Text: long + "\nsecond line"})
	if err := write(wake); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("> work:worker [service-alert] %s: %s…\n", wake.ID, strings.Repeat("界", 200)) +
		fmt.Sprintf("-- since %s\n", wake.ID)
	if got := out.String(); got != want {
		t.Fatalf("lines output =\n%s\nwant\n%s", got, want)
	}
}

// The owner's post to an agent is quiet context, not a monitor wake; the next
// waking post delivers it. Both roots read it the same way.
func TestWatchLinesHoldsOwnerDMsUntilAWake(t *testing.T) {
	seat, post, _ := watchFixture(t)
	var out bytes.Buffer
	write := watchWriter(&out, seat, false, true)
	dm := post(room.PostInput{Wake: true, From: "work", Human: true, Kind: "message", To: []string{"work:worker"}, Via: []string{"@worker"}, Text: "fix the flaky test"})
	if err := write(dm); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("an owner DM woke the monitor:\n%s", out.String())
	}
	alert := post(room.PostInput{Wake: true, From: "work:worker", Kind: "service-alert", To: []string{"work"}, Text: "Main is red"})
	if err := write(alert); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("· work [message] %s: fix the flaky test\n", dm.ID) +
		fmt.Sprintf("> work:worker [service-alert] %s: Main is red\n", alert.ID) +
		fmt.Sprintf("-- since %s\n", alert.ID)
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
