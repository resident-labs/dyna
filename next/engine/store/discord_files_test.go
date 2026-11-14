package store

import (
	"context"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// A Discord file reaches the box through /v1/files, which serves only a hash some event of the room registers.
func TestDiscordFilesAreFetchableByTheRoom(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1000, 0)
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('s','room','root','p','open','',0,NULL); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0)`); err != nil {
		t.Fatal(err)
	}
	engine := contract.Principal{Kind: contract.PrincipalEngine}
	message := contract.DiscordMessage{Channel: "200", Message: "m1", Text: "see file", Files: []contract.Attachment{{SHA256: "aaa", Name: "a.png", Media: "image/png"}}}
	if err := s.DiscordInput(ctx, contract.Event{ID: "e1", Room: "room", Time: now, From: engine, Body: message}, "s"); err != nil {
		t.Fatal(err)
	}
	retrieved := contract.DiscordFilesRetrieved{Channel: "200", Message: "m2", Files: []contract.Attachment{{SHA256: "bbb", Name: "b.md", Media: "text/plain"}}}
	d := contract.Delivery{To: contract.Principal{Kind: contract.PrincipalSession, Session: "s"}, Surface: contract.SurfaceEngine, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}
	if _, err := s.Post(ctx, contract.Event{ID: "e2", Room: "room", Time: now, From: engine, Body: retrieved}, []contract.Delivery{d}); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]string{"aaa": "image/png", "bbb": "text/plain"} {
		media, _, ok, err := s.FileMedia(ctx, hash, "room", "s", false)
		if err != nil || !ok || media != want {
			t.Fatalf("file %s: %q %v %v", hash, media, ok, err)
		}
	}
}
