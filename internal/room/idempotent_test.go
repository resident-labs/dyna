package room

import "testing"

func TestStablePostIDIsIdempotentAndCannotChangeContent(t *testing.T) {
	store := &Store{Root: t.TempDir()}
	r, err := store.Create(CreateInput{Name: "posts"})
	if err != nil {
		t.Fatal(err)
	}
	in := PostInput{ID: "msg_0123456789abcdef", From: r.Name, To: []string{r.Name}, Text: "durable batch", NeedsRoot: true, Notice: EventNotice{Kind: "ready"}}
	first, err := store.Post(r.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Post(r.ID, in)
	if err != nil || first.ID != second.ID || first.TS != second.TS {
		t.Fatalf("stable retry: %+v %+v %v", first, second, err)
	}
	posts, err := store.Messages(r.ID)
	if err != nil || len(posts) != 1 || posts[0].Notice != in.Notice || !posts[0].NeedsRoot {
		t.Fatalf("duplicate posts: %+v %v", posts, err)
	}
	in.NeedsRoot = false
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("post ID changed its root wake intent")
	}
	in.NeedsRoot = true
	in.Notice.Kind = "changes"
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("post ID was reused for different structured content")
	}
	in.Notice = first.Notice
	in.Text = "different content"
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("post ID was reused for different content")
	}
	in.ID = "../../invalid"
	if _, err = store.Post(r.ID, in); err == nil {
		t.Fatal("invalid ID accepted")
	}
}
