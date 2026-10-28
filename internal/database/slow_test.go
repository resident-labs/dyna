package database

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// stepClock makes now advance by step on each call and captures logf, so a
// test can make a transaction look slow without sleeping.
func stepClock(t *testing.T, step time.Duration) *[]string {
	t.Helper()
	var lines []string
	clock := time.Unix(0, 0)
	oldNow, oldLogf := now, logf
	now = func() time.Time { clock = clock.Add(step); return clock }
	logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { now, logf = oldNow, oldLogf })
	return &lines
}

// A writer transaction held over 100 ms is logged with its BEGIN wait, body
// and COMMIT apart, and with its process and a caller outside this package,
// so the next stall names its holder (R3.2). A fast one is not logged.
func TestSlowWriteLogsItsPartsAndHolder(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func() {
		if err := Immediate(context.Background(), db, func(*sql.Conn) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	lines := stepClock(t, 20*time.Millisecond)
	write()
	if len(*lines) != 0 {
		t.Fatalf("a 40 ms write was logged: %q", *lines)
	}
	lines = stepClock(t, 60*time.Millisecond)
	write()
	if len(*lines) != 1 {
		t.Fatalf("a 120 ms write logged %q, want one line", *lines)
	}
	line := (*lines)[0]
	for _, want := range []string{"slow write held 120ms (body 60ms, commit 60ms) after waiting 60ms for BEGIN", "database.test[", "caller "} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
	// This test is in package database, so the holder skips its own frames
	// too and lands on the test runner, which proves the skip stops at the
	// first frame outside the write path.
	if !strings.Contains(line, "caller testing.tRunner") {
		t.Errorf("line %q does not name the first caller outside the write path", line)
	}
}

// A slow read names its BEGIN wait, body and COMMIT. The wait counts toward
// the existing 1 s threshold, and a read at that threshold is not logged.
func TestSlowReadLogsItsParts(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lines := stepClock(t, 0)
	read := func(wait, body, commit time.Duration) {
		start := time.Unix(0, 0)
		times := []time.Time{start, start.Add(wait), start.Add(wait + body), start.Add(wait + body + commit)}
		now = func() time.Time {
			next := times[0]
			times = times[1:]
			return next
		}
		if err := Read(context.Background(), db, func(*sql.Tx) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	read(200*time.Millisecond, 300*time.Millisecond, 600*time.Millisecond)
	if len(*lines) != 1 {
		t.Fatalf("a 1.1 s read logged %q, want one slow read line", *lines)
	}
	want := "slow read held 1.1s (body 300ms, commit 600ms) after waiting 200ms for BEGIN"
	if !strings.Contains((*lines)[0], want) {
		t.Fatalf("line %q lacks %q", (*lines)[0], want)
	}
	read(200*time.Millisecond, 300*time.Millisecond, 500*time.Millisecond)
	if len(*lines) != 1 {
		t.Fatalf("a 1 s read was logged: %q", *lines)
	}
}
