package sessionlife

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeTurns struct {
	finished map[int]bool
	reads    map[int]int
}

func (f *fakeTurns) read(n int) ([]Steer, bool, error) {
	f.reads[n]++
	return []Steer{{Turn: n, ID: fmt.Sprintf("msg_%d", n), Delivered: f.finished[n]}}, f.finished[n], nil
}

// Steering copies a finished turn once it is not the latest, keeps reading
// unfinished turns, and Delete removes the copy.
func TestSteeringCopiesFinishedTurnsOnce(t *testing.T) {
	sessions := t.TempDir()
	const id = "ses_20261020-000000-abcdef12"
	f := &fakeTurns{finished: map[int]bool{1: true, 2: true, 4: true}, reads: map[int]int{}}
	want := func(turns int) []Steer {
		var out []Steer
		for n := 1; n <= turns; n++ {
			out = append(out, Steer{Turn: n, ID: fmt.Sprintf("msg_%d", n), Delivered: f.finished[n]})
		}
		return out
	}
	for range 2 {
		got, err := Steering(sessions, id, 4, f.read)
		if err != nil || !reflect.DeepEqual(got, want(4)) {
			t.Fatalf("Steering = %+v, %v; want %+v", got, err, want(4))
		}
	}
	// Turns 1 and 2 are copied. Turn 3 is unfinished, so turn 4 waits for it.
	if f.reads[1] != 1 || f.reads[2] != 1 || f.reads[3] != 2 || f.reads[4] != 2 {
		t.Fatalf("reads = %v", f.reads)
	}
	f.finished[3] = true
	for range 2 {
		got, err := Steering(sessions, id, 5, f.read)
		if err != nil || !reflect.DeepEqual(got, want(5)) {
			t.Fatalf("Steering = %+v, %v; want %+v", got, err, want(5))
		}
	}
	if f.reads[3] != 3 || f.reads[4] != 3 || f.reads[5] != 2 {
		t.Fatalf("turns 3 and 4 were not copied once finished: reads %v", f.reads)
	}
	if err := Delete(sessions, id); err != nil {
		t.Fatal(err)
	}
	if _, err := Steering(sessions, id, 5, f.read); err != nil || f.reads[1] != 2 {
		t.Fatalf("Delete kept the copied steering: reads %v, %v", f.reads, err)
	}
}

// A session with about 5,000 finished turns copies them once; later checks
// read one turn and one query.
func TestSteeringAt5000TurnsReadsOnlyTheLatest(t *testing.T) {
	sessions := t.TempDir()
	const id = "ses_20261020-000000-abcdef12"
	const turns = 5000
	f := &fakeTurns{finished: map[int]bool{}, reads: map[int]int{}}
	for n := 1; n < turns; n++ {
		f.finished[n] = true
	}
	start := time.Now()
	if _, err := Steering(sessions, id, turns, f.read); err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)
	start = time.Now()
	got, err := Steering(sessions, id, turns, f.read)
	if err != nil || len(got) != turns {
		t.Fatalf("Steering listed %d turns, %v", len(got), err)
	}
	again := time.Since(start)
	t.Logf("5,000 turns: first call %v, second %v", first, again)
	var reread []string
	for n, c := range f.reads {
		if c > 1 && n != turns {
			reread = append(reread, fmt.Sprint(n))
		}
	}
	if len(reread) > 0 || f.reads[turns] != 2 {
		t.Fatalf("finished turns read again: %s; latest read %d times", strings.Join(reread, ","), f.reads[turns])
	}
}
