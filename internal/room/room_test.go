package room

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCreatePostResolveAndArchive(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.Local)
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return now }}
	rec, err := s.Create(CreateInput{Name: "alpha", CreatedBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(rec.ID) {
		t.Fatalf("record = %#v", rec)
	}
	got, err := s.Resolve(rec.Name)
	if err != nil || got.ID != rec.ID {
		t.Fatalf("Resolve(name) = %#v, %v", got, err)
	}
	msg, err := s.Post(rec.ID, PostInput{From: rec.Name + ":lead", To: []string{rec.Name}, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(rec.ID)
	if err != nil || len(msgs) != 1 || msgs[0].ID != msg.ID {
		t.Fatalf("Messages() = %#v, %v", msgs, err)
	}
	if rec.Archived(now.Add(61*time.Minute), false) != true {
		t.Fatal("inactive room did not archive")
	}
	if rec.Archived(now.Add(61*time.Minute), true) {
		t.Fatal("room with live work archived")
	}
	if _, err := s.Post(rec.ID, PostInput{From: "x", To: []string{rec.Name}, Text: strings.Repeat("x", MaxMessageBytes+1)}); err == nil {
		t.Fatal("oversize message accepted")
	}
}

func TestParseAddress(t *testing.T) {
	a, err := ParseAddress("amber-otter-42:decoder")
	if err != nil || a.Room != "amber-otter-42" || a.Agent != "decoder" || a.String() != "amber-otter-42:decoder" {
		t.Fatalf("ParseAddress() = %#v, %v", a, err)
	}
	if _, err := ParseAddress("bad name:decoder"); err == nil {
		t.Fatal("invalid room name accepted")
	}
	if _, err := ParseAddress("amber-otter-42:bad:name"); err == nil {
		t.Fatal("invalid agent accepted")
	}
}

func TestResolveRequiresARoom(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if _, err := s.Resolve(""); err == nil {
		t.Fatal(`Resolve("") fell back to a default room`)
	}
	if _, err := s.Resolve("global"); err == nil {
		t.Fatalf("Resolve(global) created a room: %v", err)
	}
	if _, err := s.Create(CreateInput{Name: "global"}); err == nil {
		t.Fatal("reserved global room name was accepted")
	}
	if _, err := ParseAddress("global:lead"); err == nil {
		t.Fatal("reserved global room address was accepted")
	}
}

func TestHeaderMarksReaderOwnerAndGroup(t *testing.T) {
	m := Message{ID: "msg_1", From: "r:lead", To: []string{"r:decoder", "r:parser", "r"}, Via: []string{"@children", "@root"}, Text: "hi"}
	got := m.Frame(FrameOptions{Room: "r", Reader: "r:parser", Reply: "r:lead"})
	want := "[dyna message msg_1 from r:lead]\n" +
		"To: @children @root = r:decoder, r:parser (you), r (root)\n" +
		"Reply: dyna post @r:lead \"…\"   (to all: add @children @root)\n\nhi"
	if got != want {
		t.Fatalf("frame =\n%s\nwant\n%s", got, want)
	}
	direct := Message{ID: "msg_2", From: "r:lead", To: []string{"r:decoder"}, Via: []string{"@decoder"}, Text: "x"}
	if got := direct.Header(FrameOptions{Room: "r", Reader: "r:decoder"}); got != "[dyna message msg_2 from r:lead]\nTo: r:decoder (you)" {
		t.Fatalf("direct header = %q", got)
	}
	var many []string
	for i := 0; i < 11; i++ {
		many = append(many, "r:a"+strings.Repeat("x", i))
	}
	human := Message{ID: "msg_3", From: "r", Human: true, To: []string{"r:decoder"}}
	if got := human.Header(FrameOptions{Room: "r"}); !strings.HasPrefix(got, "[dyna message msg_3 from r (human)]\n") {
		t.Fatalf("human header = %q", got)
	}
	if got := (Message{ID: "m", From: "r", To: many}).Header(FrameOptions{Room: "r"}); !strings.Contains(got, "+3 more") {
		t.Fatalf("long To line = %q", got)
	}
}

func TestSteeringFrameFitsLimitAndPointsToPost(t *testing.T) {
	const limit = 2000
	m := Message{ID: "msg_abc", From: "r:lead", To: []string{"r:decoder"}, Via: []string{"@decoder"}}
	o := FrameOptions{Room: "r", Reader: "r:decoder", Reply: "r:lead"}
	m.Text = "short"
	if got, cut := m.SteeringFrame(o, limit); cut || got != m.Frame(o) {
		t.Fatalf("short frame = %q cut=%v", got, cut)
	}
	// Multi-byte text: the cut must land on a UTF-8 boundary.
	m.Text = strings.Repeat("é", 3000)
	got, cut := m.SteeringFrame(o, limit)
	if !cut || len(got) > limit || !utf8.ValidString(got) {
		t.Fatalf("pointer frame: cut=%v len=%d valid=%v", cut, len(got), utf8.ValidString(got))
	}
	if !strings.HasPrefix(got, m.Header(o)+"\n\n") {
		t.Fatalf("pointer frame lost its header: %q", got[:80])
	}
	body := strings.TrimPrefix(got, m.Header(o)+"\n\n")
	match := regexp.MustCompile(`…\n\[Truncated: (\d+) of (\d+) bytes\. Full text: dyna room read r msg_abc\]$`).FindStringSubmatchIndex(body)
	if match == nil {
		t.Fatalf("no pointer trailer in %q", body[len(body)-120:])
	}
	prefix := body[:match[0]]
	shown, _ := strconv.Atoi(body[match[2]:match[3]])
	total, _ := strconv.Atoi(body[match[4]:match[5]])
	if shown != len(prefix) || total != len(m.Text) || !strings.HasPrefix(m.Text, prefix) {
		t.Fatalf("shown=%d prefix=%d total=%d", shown, len(prefix), total)
	}
	if shown < 1000 {
		t.Fatalf("only %d bytes of text fit", shown)
	}
}

func TestQuestionMarker(t *testing.T) {
	for _, tc := range []struct{ kind, text, want string }{
		{"message", "[QUESTION q12] Which fix?", "q12"},
		{"message", "[ANSWER q12] Small fix.", ""},
		{"message", "Example [QUESTION q12]", ""},
		{"completed", "[QUESTION q12] Example", ""},
	} {
		if got := (Message{Kind: tc.kind, Text: tc.text}).QuestionID(); got != tc.want {
			t.Errorf("%+v: got %q", tc, got)
		}
	}
}

func TestPostStoresWakeIntentWithStableContent(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	input := PostInput{Wake: true, ID: "msg_0123456789abcdef", From: rec.Name, To: []string{"work:target"}, Text: "queued input"}
	msg, err := s.Post(rec.ID, input)
	if err != nil || !msg.Wake {
		t.Fatalf("wake intent: %+v %v", msg, err)
	}
	stored, err := s.Message(rec.ID, msg.ID)
	if err != nil || !stored.Wake {
		t.Fatalf("stored wake intent: %+v %v", stored, err)
	}
	if _, err := s.Post(rec.ID, input); err != nil {
		t.Fatal(err)
	}
	input.Wake = false
	if _, err := s.Post(rec.ID, input); err == nil {
		t.Fatal("stable post changed its wake intent")
	}
}
