package room

import "testing"

func TestStripSelfLabel(t *testing.T) {
	cases := []struct{ from, text, want string }{
		{"dyna-agent:desktop-polish", "desktop-polish: the build passed.", "the build passed."},
		{"dyna-agent:desktop-polish", "Desktop-Polish: done", "done"},
		{"dyna-agent:desktop-polish", "dyna-agent:desktop-polish: done", "done"},
		{"dyna-agent:lead", "**lead:** ready\nnext line", "ready\nnext line"},
		{"dyna-agent:lead", "**lead**: ready", "ready"},
		{"dyna-agent", "dyna-agent: owner note", "owner note"},
		// Not the sender, not a label, or nothing left: keep the text.
		{"dyna-agent:lead", "reviewer: please check", "reviewer: please check"},
		{"dyna-agent:lead", "lead:er is a word", "lead:er is a word"},
		{"dyna-agent:lead", "lead:", "lead:"},
		{"dyna-agent:lead", "the lead: fine", "the lead: fine"},
	}
	for _, c := range cases {
		if got := stripSelfLabel(c.from, c.text); got != c.want {
			t.Errorf("stripSelfLabel(%q, %q) = %q, want %q", c.from, c.text, got, c.want)
		}
	}
}

func TestPostStripsSelfLabelOnMessagesOnly(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := DefaultStore()
	rec, err := s.Create(CreateInput{Name: "alpha", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "lead: ready"})
	if err != nil {
		t.Fatal(err)
	}
	if msg.Text != "ready" {
		t.Fatalf("message text = %q, want %q", msg.Text, "ready")
	}
	done, err := s.Post(rec.ID, PostInput{From: "alpha:lead", Kind: "completed", To: []string{"alpha"}, Turn: 1, Text: "lead: kept in a completion"})
	if err != nil {
		t.Fatal(err)
	}
	if done.Text != "lead: kept in a completion" {
		t.Fatalf("completion text = %q, want it unchanged", done.Text)
	}
}
