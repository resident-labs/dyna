package room

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// A tiny valid PNG (1x1).
var pngBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}

func attachRoom(t *testing.T, name string) (*Store, Record) {
	t.Helper()
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return s, rec
}

func TestPutAttachmentSavesOnceAndSniffs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	setContentLimits(t, 1024, 1024)
	s, rec := attachRoom(t, "alpha")
	a, err := s.PutAttachment(rec.ID, "../../shot.png", pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "shot.png" || a.MIME != "image/png" || !a.Image() || a.Size != int64(len(pngBytes)) || len(a.ID) != 24 || !strings.HasPrefix(a.SHA256, a.ID) {
		t.Fatalf("attachment = %+v", a)
	}
	again, err := s.PutAttachment(rec.ID, "other.png", pngBytes)
	if err != nil || again.ID != a.ID {
		t.Fatalf("same bytes must keep one id: %+v %v", again, err)
	}
	path, err := s.AttachmentPath(rec.ID, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, pngBytes) {
		t.Fatal("saved bytes differ")
	}
	txt, err := s.PutAttachment(rec.ID, "notes.md", []byte("# hi @lead"))
	if err != nil || txt.MIME != "text/markdown" || !txt.Text() {
		t.Fatalf("text attachment = %+v %v", txt, err)
	}
	if _, err := s.PutAttachment(rec.ID, "e", nil); err == nil {
		t.Fatal("an empty file must fail")
	}
	if _, err := s.PutAttachment(rec.ID, "big", make([]byte, 1025)); err == nil {
		t.Fatal("a file over the limit must fail")
	}
}

func TestPutAttachmentConcurrentSameBytes(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a, err := s.PutAttachment(rec.ID, "shot.png", pngBytes)
			if err != nil {
				t.Error(err)
			}
			ids[i] = a.ID
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids differ: %v", ids)
		}
	}
	dir, _ := s.FilesDir(rec.ID)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("files dir has %d entries, want 1 (no temp leftovers)", len(entries))
	}
}

func TestPostWithAttachmentsOnly(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	a, _ := s.PutAttachment(rec.ID, "shot.png", pngBytes)
	msg, err := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha:lead"}, Attachments: []Attachment{{ID: a.ID, Name: "my shot.png", MIME: "text/evil", Size: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	got := msg.Attachments[0]
	if got.Name != "my shot.png" || got.MIME != "image/png" || got.Size != a.Size || got.SHA256 != a.SHA256 {
		t.Fatalf("stored attachment = %+v; the record must come from the saved bytes", got)
	}
	back, err := s.Message(rec.ID, msg.ID)
	if err != nil || len(back.Attachments) != 1 || back.Attachments[0].ID != a.ID {
		t.Fatalf("read back = %+v %v", back, err)
	}
	if _, err := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha:lead"}}); err == nil {
		t.Fatal("a post with no text and no attachments must fail")
	}
}

func TestPostRejectsAttachmentsFromAnotherRoom(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	other, err := s.Create(CreateInput{Name: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.PutAttachment(other.ID, "shot.png", pngBytes)
	if _, err := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha:lead"}, Text: "hi", Attachments: []Attachment{a}}); err == nil {
		t.Fatal("a post must not name a file saved in another room")
	}
	if _, err := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha:lead"}, Text: "hi", Attachments: []Attachment{{ID: "../../etc/passwd"}}}); err == nil {
		t.Fatal("a malformed id must fail")
	}
	msgs, _ := s.Messages(rec.ID)
	if len(msgs) != 0 {
		t.Fatalf("a rejected post stored %d messages", len(msgs))
	}
}

func TestFrameListsAttachmentsWithPaths(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	a, _ := s.PutAttachment(rec.ID, "shot.png", pngBytes)
	msg, _ := s.Post(rec.ID, PostInput{From: "alpha", Human: true, To: []string{"alpha:lead"}, Attachments: []Attachment{a}})
	dir, _ := s.FilesDir(rec.ID)
	frame := msg.Frame(FrameOptions{Room: "alpha", Reader: "alpha:lead", FilesDir: dir})
	for _, want := range []string{"Attachments (1)", "shot.png · image/png", dir + "/" + a.ID + ".png", "tool that reads image files", "dyna attachment get alpha"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame lacks %q:\n%s", want, frame)
		}
	}
	remote := msg.Frame(FrameOptions{Room: "alpha"})
	if !strings.Contains(remote, "dyna attachment get alpha "+a.ID) {
		t.Fatalf("without a files dir the frame must give the get command:\n%s", remote)
	}
}

func TestSteeringFrameKeepsAttachmentsUnderTheLimit(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	var atts []Attachment
	for i := 0; i < MaxPostAttachments; i++ {
		a, err := s.PutAttachment(rec.ID, fmt.Sprintf("%s-%d.txt", strings.Repeat("very-long-file-name", 4), i), []byte(fmt.Sprintf("file %d", i)))
		if err != nil {
			t.Fatal(err)
		}
		atts = append(atts, a)
	}
	caption := strings.Repeat("caption words ", 300) // over 4 KB
	msg, err := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha:lead"}, Text: caption, Attachments: atts})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.FilesDir(rec.ID)
	frame, cut := msg.SteeringFrame(FrameOptions{Room: "alpha", Reader: "alpha:lead", FilesDir: dir}, 2000)
	if !cut || len(frame) > 2000 {
		t.Fatalf("frame is %d bytes (cut=%v), want <= 2000", len(frame), cut)
	}
	if !strings.Contains(frame, "Attachments (8)") || !strings.Contains(frame, dir+"/"+atts[0].ID+".txt") {
		t.Fatalf("the attachment list must survive a long caption:\n%s", frame)
	}
	if !strings.Contains(frame, "more: dyna room read alpha "+msg.ID) {
		t.Fatalf("files that do not fit must point to the full post:\n%s", frame)
	}
}

func TestSanitizeAttachmentName(t *testing.T) {
	for in, want := range map[string]string{
		"a/b/c.txt":       "c.txt",
		`C:\x\y.png`:      "y.png",
		"..":              "file",
		"bad\x00name.txt": "bad_name.txt",
	} {
		if got := SanitizeAttachmentName(in); got != want {
			t.Errorf("SanitizeAttachmentName(%q) = %q, want %q", in, got, want)
		}
	}
	long := SanitizeAttachmentName(strings.Repeat("x", 200) + ".png")
	if len(long) > maxAttachNameBytes+3 || !strings.HasSuffix(long, ".png") {
		t.Errorf("long name = %q", long)
	}
}

func TestSteeringFrameFitsWithLongRecipientHeader(t *testing.T) {
	s, rec := attachRoom(t, "alpha")
	var atts []Attachment
	for i := 0; i < 3; i++ {
		a, err := s.PutAttachment(rec.ID, fmt.Sprintf("%s-%d.txt", strings.Repeat("n", 70), i), []byte(fmt.Sprintf("f%d", i)))
		if err != nil {
			t.Fatal(err)
		}
		atts = append(atts, a)
	}
	var to []string
	for i := 0; i < 8; i++ {
		to = append(to, fmt.Sprintf("alpha:%s-%d", strings.Repeat("recipient", 7), i))
	}
	msg, err := s.Post(rec.ID, PostInput{From: "alpha", To: to, Text: strings.Repeat("long caption ", 200), Attachments: atts})
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := s.FilesDir(rec.ID)
	for _, limit := range []int{2000, 1200, 900} {
		frame, _ := msg.SteeringFrame(FrameOptions{Room: "alpha", Reader: to[0], Reply: "alpha", FilesDir: dir}, limit)
		if len(frame) > limit {
			t.Fatalf("limit %d: frame is %d bytes:\n%s", limit, len(frame), frame)
		}
		if !strings.Contains(frame, "Attachments (3)") {
			t.Fatalf("limit %d: the frame must still say there are attachments:\n%s", limit, frame)
		}
	}
}
