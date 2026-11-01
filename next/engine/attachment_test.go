package engine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// A real PNG signature, which is all the frame reads of a file.
const pngHead = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

func uploadFile(t *testing.T, s *Service, room contract.RoomID, name, media, body string) contract.Attachment {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/files?name="+name, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", media)
	rec := httptest.NewRecorder()
	s.Files(contract.Principal{Kind: contract.PrincipalOwner}, room).ServeHTTP(rec, req)
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	return file
}

func TestFrameNamesEachAttachment(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	files := []contract.Attachment{
		uploadFile(t, s, room, "image.png", "image/png", pngHead),
		uploadFile(t, s, room, "image.png", "image/png", pngHead+"again"), // same name, other bytes
		uploadFile(t, s, room, "photo.bin", "application/octet-stream", pngHead),
		uploadFile(t, s, room, "fake.png", "image/png", "not an image"),
		uploadFile(t, s, room, "report+final.pdf", "application/pdf", "%PDF-1.7"),
	}
	huge := uploadFile(t, s, room, "huge.png", "image/png", pngHead)
	huge.Name, huge.SHA256 = "huge.png", strings.Repeat("a", 64)
	if err := os.WriteFile(s.layout.File(room, huge.SHA256), []byte(pngHead), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(s.layout.File(room, huge.SHA256), maxShownImage+1); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	sent, problem := s.Post(ctx, owner, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "look", Attachments: append(files, huge)})
	if problem != nil {
		t.Fatal(problem)
	}
	ch, problem := testChannel(t, s, ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker})
	if problem != nil {
		t.Fatal(problem)
	}
	var input contract.TurnInput
	for _, candidate := range (<-ch).Body.(contract.TurnStart).Inputs {
		if len(candidate.Files) > 0 {
			input = candidate
		}
	}
	dir := filepath.Join("/box/attachments", strconvSeq(sent.Seq))
	frame, body, _ := strings.Cut(input.Text, "\n")
	for _, part := range []string{
		"Attached: " + dir + "/image.png (image/png, 1 KB, shown to you), ",
		dir + "/image-2.png (image/png, 1 KB, shown to you), ",
		dir + "/photo.bin (image/png, 1 KB, shown to you), ", // the bytes decide, not the label
		dir + "/fake.png (image/png, 1 KB), ",
		dir + "/report_final.pdf (application/pdf, 1 KB), ",
		dir + "/huge.png (image/png, 16.0 MB, too large to show you, so read the file). ",
	} {
		if !strings.Contains(frame, part) {
			t.Errorf("missing %q in %q", part, frame)
		}
	}
	if body != "look" {
		t.Fatalf("body %q", body)
	}
	if len(input.Files) != 6 || input.Files[0].SHA256 != files[0].SHA256 || input.Files[1].Path != dir+"/image-2.png" || input.Files[5].Bytes != maxShownImage+1 {
		t.Fatalf("files %+v", input.Files)
	}
}

func strconvSeq(seq int64) string {
	path := (box.Layout{}).BoxAttachments(seq, []contract.Attachment{{Name: "x"}})[0]
	return filepath.Base(filepath.Dir(path))
}

// The extension reads the frame to find its images. This is its pattern against what the engine writes.
func TestExtensionFindsTheImagesInAFrame(t *testing.T) {
	source, err := os.ReadFile("../box/agent/extension.js")
	if err != nil {
		t.Fatal(err)
	}
	literal := regexp.MustCompile(`(?m)^const framedImage = /(.*)/g;$`).FindSubmatch(source)
	if literal == nil {
		t.Fatal("no framedImage in the extension")
	}
	pattern := regexp.MustCompile(strings.ReplaceAll(string(literal[1]), `\/`, `/`))
	frame := "[Dyna] From the owner, in your DM. Attached: /box/attachments/9/a.png (image/png, 1.0 MB, shown to you), /box/attachments/9/b.pdf (application/pdf, 1 KB), /box/attachments/9/c.jpg (image/jpeg, 20.0 MB, too large to show you, so read the file), /box/attachments/9/d-2.webp (image/webp, 12 KB, shown to you). Your final response goes to the owner in your DM."
	var found []string
	for _, match := range pattern.FindAllStringSubmatch(frame, -1) {
		found = append(found, match[1]+" "+match[2])
	}
	if want := "/box/attachments/9/a.png image/png,/box/attachments/9/d-2.webp image/webp"; strings.Join(found, ",") != want {
		t.Fatalf("found %q", found)
	}
}

// A mention can deliver a post to another room. That session reads this file and no other session there does.
func TestDeliveryGrantsThatPostsFileAcrossRooms(t *testing.T) {
	s, roomA, sessionA, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	roomB, e := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	named, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: roomB.Room, Name: "named", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	bystander, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: roomB.Room, Name: "bystander", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	file := uploadFile(t, s, roomA, "note.txt", "text/plain", "for named only")
	sender := contract.Principal{Kind: contract.PrincipalSession, Session: sessionA}
	if _, problem := s.Post(ctx, sender, contract.PostRequest{Room: roomA, Text: "see this", Attachments: []contract.Attachment{file}, Mentions: []string{"other:named"}}); problem != nil {
		t.Fatal(problem)
	}
	read := func(session contract.SessionID) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Files(contract.Principal{Kind: contract.PrincipalSession, Session: session}, roomB.Room).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/"+file.SHA256, nil))
		return rec
	}
	if rec := read(named.Session); rec.Code != http.StatusOK || rec.Body.String() != "for named only" {
		t.Fatalf("named: %d %s", rec.Code, rec.Body)
	}
	if rec := read(bystander.Session); rec.Code != http.StatusNotFound {
		t.Fatalf("bystander: %d %s", rec.Code, rec.Body)
	}
}

// A steer the box hands back is not read: it starts the next turn, with its files.
func TestUnreadSteerStartsTheNextTurn(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	ch, problem := testChannel(t, s, ctx, caller)
	if problem != nil {
		t.Fatal(problem)
	}
	start := (<-ch).Body.(contract.TurnStart)
	file := uploadFile(t, s, room, "note.txt", "text/plain", "steered")
	sent, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: worker}, Text: "and this", Attachments: []contract.Attachment{file}})
	if problem != nil {
		t.Fatal(problem)
	}
	steer := (<-ch).Body.(contract.TurnSteer)
	if int64(steer.Input.Delivery) != sent.Seq || len(steer.Input.Files) != 1 {
		t.Fatalf("steer %+v", steer)
	}
	for _, input := range start.Inputs {
		if _, problem := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: input.Delivery}); problem != nil {
			t.Fatal(problem)
		}
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: contract.TurnCompleted, Model: "model", Final: "done", Unread: []contract.DeliverySeq{steer.Input.Delivery}}); problem != nil {
		t.Fatal(problem)
	}
	next, ok := (<-ch).Body.(contract.TurnStart)
	if !ok || next.Turn == start.Turn || len(next.Inputs) != 1 || next.Inputs[0].Delivery != steer.Input.Delivery || len(next.Inputs[0].Files) != 1 {
		t.Fatalf("next turn %+v", next)
	}
}
