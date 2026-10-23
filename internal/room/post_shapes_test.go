package room

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoricalCardAndShellShapesAreCanonical(t *testing.T) {
	s, id, path, _ := historicalWakeFixture(t)
	raw := []byte("{\"id\":\"card\",\"kind\":\"message\",\"wake\":false,\"attachments\":[{\"render\":\"html\",\"unknown\":true}]}\n{\"id\":\"shell\",\"kind\":\"message\",\"from\":\"shell:sh_old\",\"text\":\"Exited Output: dyna shell tail sh_old\"}\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	posts, next, err := s.ReadFrom(id, 0)
	if err != nil || len(posts) != 2 {
		t.Fatalf("posts = %+v, %v", posts, err)
	}
	if posts[0].Attachments[0].Version != 1 || posts[0].Wake || posts[1].Text != "Exited\nOutput: dyna shell tail sh_old" || next != int64(len(raw)) {
		t.Fatalf("canonical posts = %+v, cursor %d", posts, next)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("source changed: %v", err)
	}
	db, err := s.recordsDB()
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err := db.QueryRow("SELECT body FROM room_post_normalizations WHERE room_id=? AND source_start=0", id).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"unknown":true`)) {
		t.Fatalf("unknown field lost: %s", body)
	}
}

func TestQuestionProjectionPreservesCursorAndOmitsDuplicateAnswer(t *testing.T) {
	s, id, path, _ := historicalWakeFixture(t)
	first := "{\"id\":\"q\",\"kind\":\"question\",\"text\":\"[QUESTION q1] Which fix?\"}\n"
	raw := []byte(first + "{\"id\":\"a\",\"kind\":\"message\",\"text\":\"[ANSWER q1] Small.\"}\n{\"id\":\"dup\",\"kind\":\"question-answered\",\"text\":\"q1 answered\"}\n")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{0, int64(len(first))} {
		posts, next, err := s.ReadFrom(id, offset)
		want := 2 - int(offset/int64(len(first)))
		if err != nil || len(posts) != want || next != int64(len(raw)) {
			t.Fatalf("offset %d: posts = %+v, cursor %d, %v", offset, posts, next, err)
		}
		for _, p := range posts {
			if p.Kind != "message" {
				t.Fatalf("kind = %q", p.Kind)
			}
		}
	}
	if posts, _, _ := s.ReadFrom(id, 0); posts[0].QuestionID() != "q1" {
		t.Fatalf("question marker lost: %+v", posts[0])
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("source changed: %v", err)
	}
}
