package room

import (
	"strings"
	"testing"
)

func TestUpdatesAddVersionsToOneVisual(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	put := func(doc string) Attachment {
		a, err := s.PutAttachment(rec.ID, "v.html", []byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return Attachment{ID: a.ID, Name: "v.html", Render: RenderHTML}
	}
	post := func(text string, a Attachment) (Message, error) {
		return s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: text, Attachments: []Attachment{a}})
	}
	first, err := post("v1", put("<title>Chart</title><p>1</p>"))
	if err != nil {
		t.Fatal(err)
	}
	if a := first.Attachments[0]; a.Visual != "" || a.Version != 1 {
		t.Fatalf("a new card is version 1 of itself: %+v", a)
	}
	up := put("<title>Chart</title><p>2</p>")
	up.Visual = first.ID
	second, err := post("v2", up)
	if err != nil {
		t.Fatal(err)
	}
	if a := second.Attachments[0]; a.Visual != first.ID || a.Version != 2 {
		t.Fatalf("second = %+v", a)
	}
	// An update may name any version; it joins the first post's visual.
	up = put("<title>Chart</title><p>3</p>")
	up.Visual = second.ID
	third, err := post("v3", up)
	if err != nil {
		t.Fatal(err)
	}
	if a := third.Attachments[0]; a.Visual != first.ID || a.Version != 3 {
		t.Fatalf("third = %+v", a)
	}

	if got := third.manifest(FrameOptions{Room: "alpha"}, 0); !strings.Contains(got, `HTML card "Chart", visual `+first.ID+" v3") {
		t.Fatalf("readers must see which visual and version a card is:\n%s", got)
	}

	plain, _ := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Text: "no card"})
	for name, target := range map[string]string{"missing": "msg_0000000000000000", "not a card": plain.ID} {
		up := put("<p>bad " + name + "</p>")
		up.Visual = target
		if _, err := post("bad", up); err == nil {
			t.Errorf("%s: an update must name a visual in this room", name)
		}
	}
	file, _ := s.PutAttachment(rec.ID, "notes.txt", []byte("notes"))
	if _, err := post("bad", Attachment{ID: file.ID, Name: "notes.txt", Visual: first.ID}); err == nil {
		t.Error("only an HTML card can be a visual version")
	}
}

func TestRetriedUpdateKeepsItsTarget(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	card := func(doc, visual string) []Attachment {
		a, err := s.PutAttachment(rec.ID, "v.html", []byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return []Attachment{{ID: a.ID, Name: "v.html", Render: RenderHTML, Visual: visual}}
	}
	in := func(id string, atts []Attachment) PostInput {
		return PostInput{ID: id, From: "alpha:lead", To: []string{"alpha"}, Text: "t", Attachments: atts}
	}
	one, _ := s.Post(rec.ID, in("", card("<p>1</p>", "")))
	two, _ := s.Post(rec.ID, in("", card("<p>2</p>", "")))
	up, err := s.Post(rec.ID, in("msg_00000000000000aa", card("<p>3</p>", one.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Post(rec.ID, in("msg_00000000000000aa", card("<p>3</p>", one.ID))); err != nil {
		t.Fatalf("an exact retry must return the stored post: %v", err)
	}
	if _, err := s.Post(rec.ID, in("msg_00000000000000aa", card("<p>3</p>", up.ID))); err != nil {
		t.Fatalf("a retry naming another version of the same visual is the same post: %v", err)
	}
	if _, err := s.Post(rec.ID, in("msg_00000000000000aa", card("<p>3</p>", two.ID))); err == nil {
		t.Fatal("a retry that targets another visual must fail")
	}
}
