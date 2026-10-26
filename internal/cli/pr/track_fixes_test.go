package pr

import "testing"

// D-277: --fixes "" must clear the links, which the engine reads as a non-nil empty list.
func TestTrackFixListEmptyClearsLinks(t *testing.T) {
	cleared, err := trackFixList("")
	if err != nil || cleared == nil || len(cleared) != 0 {
		t.Fatalf("empty --fixes = %#v, %v; want a non-nil empty list", cleared, err)
	}
	set, err := trackFixList("D-5,#207")
	if err != nil || len(set) != 2 {
		t.Fatalf("--fixes D-5,#207 = %v, %v", set, err)
	}
}
