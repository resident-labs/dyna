package watch

import "testing"

func TestMergeParallelLimitDefaultsAndPersists(t *testing.T) {
	f := mergeTrialEnv(t, 1, 2, 3)
	id := f.e.author.RoomID
	got, err := f.e.s.MergeSettings(id)
	if err != nil || got.MaxParallel != 3 {
		t.Fatalf("defaults=%+v error=%v", got, err)
	}
	if err := f.e.s.SetMergeMaxParallel(id, 1); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, -1} {
		if err := f.e.s.SetMergeMaxParallel(id, n); err == nil {
			t.Fatalf("accepted invalid limit %d", n)
		}
	}
	got, err = f.e.s.MergeSettings(id)
	if err != nil || got.MaxParallel != 1 {
		t.Fatalf("saved=%+v error=%v", got, err)
	}
	f.uniqueHeads = true
	f.trialCI = map[int]string{3: "failure", 1: "pending", 2: "pending"}
	for range 3 {
		if _, err := f.e.s.MergeTick(t.Context()); err != nil {
			t.Fatal(err)
		}
		requestFixtureBisect(t, f)
	}
	if len(f.branches) != 2 {
		t.Fatalf("one active prefix expected, got %v", f.branches)
	}
	st, err := f.e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	b := st.MergeTrials["github.com/owner/repo"]
	if b.MaxParallel != 1 || b.Stacks[1].Phase != "queued" {
		t.Fatalf("stack state=%+v", b)
	}
}
