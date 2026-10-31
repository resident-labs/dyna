package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptSentPosts(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, room := range []contract.RoomID{"r", "other"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.writer.Exec("INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0)")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		appendEvent := func(id contract.EventID, room contract.RoomID, at int64, from contract.Principal, body contract.EventBody) (int64, error) {
			return s.AppendEvent(tx, contract.Event{ID: id, Room: room, Time: time.Unix(at, 0), From: from, Body: body})
		}
		owner := contract.Principal{Kind: contract.PrincipalOwner}
		session := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
		seq, err := appendEvent("start", "r", 1, owner, contract.TurnStarted{Session: "s", Turn: "t"})
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('t','s',0,'','',0,0,0,0,?)", seq); err != nil {
			return err
		}
		if _, err = appendEvent("later", "r", 3, session, contract.PostCreated{Text: "later"}); err != nil {
			return err
		}
		if _, err = appendEvent("cross", "other", 2, session, contract.PostCreated{Text: "cross"}); err != nil {
			return err
		}
		if _, err = appendEvent("end", "r", 4, session, contract.TurnEnded{Session: "s", Turn: "t", Outcome: contract.TurnCompleted}); err != nil {
			return err
		}
		_, err = appendEvent("after", "r", 5, session, contract.PostCreated{Text: "after"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	posts, err := s.TranscriptSentPosts(ctx, "s", "t", 1, 0)
	if err != nil || len(posts) != 1 || posts[0].ID != "cross" {
		t.Fatalf("first page = %+v, %v", posts, err)
	}
	posts, err = s.TranscriptSentPosts(ctx, "s", "t", 1, 1)
	if err != nil || len(posts) != 1 || posts[0].ID != "later" {
		t.Fatalf("second page = %+v, %v", posts, err)
	}
	event, found, err := s.TranscriptTurnPost(ctx, "s", "t", "cross")
	if err != nil || !found || event.Room != "other" {
		t.Fatalf("cross lookup = %+v, %v, %v", event, found, err)
	}
	_, found, err = s.TranscriptTurnPost(ctx, "s", "t", "after")
	if err != nil || found {
		t.Fatalf("out of turn = %v, %v", found, err)
	}
}

func TestTranscriptTurnIndex(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,1,1);
		INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0)`)
	if err != nil {
		t.Fatal(err)
	}
	starts := []TranscriptTurnStart{{Turn: "a", Start: 0}, {Turn: "b", Start: 10}, {Turn: "c", Start: 20}}
	if err := s.CommitTranscriptOffset(ctx, "s", "file", 30, starts); err != nil {
		t.Fatal(err)
	}
	ranges, err := s.TranscriptTurnRanges(ctx, "s", "b")
	if err != nil || len(ranges) != 1 || ranges[0].Start != 10 || ranges[0].End == nil || *ranges[0].End != 20 {
		t.Fatalf("ranges = %+v, err = %v", ranges, err)
	}
	turns, err := s.TranscriptTurnsIntersecting(ctx, "s", "file", 15, 21)
	if err != nil || !reflect.DeepEqual(turns, []contract.TurnID{"b", "c"}) {
		t.Fatalf("turns = %v, err = %v", turns, err)
	}
	turns, err = s.TranscriptTurnsIntersecting(ctx, "s", "file", 10, 20)
	if err != nil || !reflect.DeepEqual(turns, []contract.TurnID{"b"}) {
		t.Fatalf("boundary turns = %v, err = %v", turns, err)
	}
	if err := s.CommitTranscriptOffset(ctx, "s", "file", 30, []TranscriptTurnStart{{Turn: "b", Start: 15}}); err != nil {
		t.Fatal(err)
	}
	ranges, err = s.TranscriptTurnRanges(ctx, "s", "b")
	if err != nil || len(ranges) != 1 || ranges[0].Start != 10 {
		t.Fatalf("repeated start = %+v, err = %v", ranges, err)
	}
	// A conflicting byte position must roll back the offset and all starts.
	err = s.CommitTranscriptOffset(ctx, "s", "file", 40, []TranscriptTurnStart{{Turn: "d", Start: 30}, {Turn: "e", Start: 10}})
	if err == nil {
		t.Fatal("expected conflicting start to fail")
	}
	offset, err := s.TranscriptOffset(ctx, "s", "file")
	if err != nil || offset != 30 {
		t.Fatalf("offset = %d, err = %v", offset, err)
	}
	if err := s.CommitTranscriptOffset(ctx, "s", "file", 10, nil); err != nil {
		t.Fatal(err)
	}
	offset, err = s.TranscriptOffset(ctx, "s", "file")
	if err != nil || offset != 30 {
		t.Fatalf("stale backfill lowered offset to %d: %v", offset, err)
	}
	ranges, err = s.TranscriptTurnRanges(ctx, "s", "d")
	if err != nil || len(ranges) != 0 {
		t.Fatalf("rolled-back ranges = %v, err = %v", ranges, err)
	}
}
