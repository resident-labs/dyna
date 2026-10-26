package parentcheckin

import (
	"testing"

	prstate "dyna-agent/internal/pr/state"
)

func TestOpenChangeLink(t *testing.T) {
	cases := []struct {
		name string
		pr   prstate.Record
		want string
	}{
		{"github", prstate.Record{Number: 42, State: "open"}, "open PR #42: open"},
		{"local", prstate.Record{Kind: "local", Key: "change_a1", ChangeID: "change_a1", Title: "Fix order", State: "open"}, `open local change "Fix order" (change_a1): open`},
		{"local without change id", prstate.Record{Kind: "local", Key: "change_b2", Title: "T", State: "open"}, `open local change "T" (change_b2): open`},
	}
	for _, c := range cases {
		if got := openChangeLink(c.pr); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
