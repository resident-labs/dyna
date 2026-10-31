package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestSessionWorkPagesTurns(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateRoom(ctx, "r", "r", time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec("INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0)")
	if err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		appendEvent := func(id string, from contract.Principal, body contract.EventBody) (int64, error) {
			return s.AppendEvent(tx, contract.Event{ID: contract.EventID(id), Time: time.Unix(1, 0), Room: "r", From: from, Body: body})
		}
		owner := contract.Principal{Kind: contract.PrincipalOwner}
		session := contract.Principal{Kind: contract.PrincipalSession, Session: "s"}
		for i := 1; i <= 3; i++ {
			input := fmt.Sprintf("input%d", i)
			turn := contract.TurnID(fmt.Sprintf("turn%d", i))
			if _, err := appendEvent(input, owner, contract.PostCreated{Text: input}); err != nil {
				return err
			}
			seq, err := appendEvent(fmt.Sprintf("start%d", i), owner, contract.TurnStarted{Session: "s", Turn: turn, Inputs: []contract.EventID{contract.EventID(input)}})
			if err != nil {
				return err
			}
			_, err = tx.Exec("INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES(?, 's', 0, '', '', 0, 0, 0, 0, ?)", turn, seq)
			if err != nil {
				return err
			}
			if _, err = appendEvent(fmt.Sprintf("post%d", i), session, contract.PostCreated{Text: "post"}); err != nil {
				return err
			}
			if i == 2 {
				if _, err = appendEvent(fmt.Sprintf("end%d", i), session, contract.TurnEnded{Session: "s", Turn: turn, Outcome: contract.TurnCompleted}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	page, more, total, err := s.SessionWork(ctx, "s", "", 2)
	if err != nil || total != 3 || !more || len(page) != 2 || page[0].Started.ID != "start3" || page[0].Ended != nil || len(page[0].Posts) != 1 || len(page[0].Inputs) != 1 || page[1].Ended == nil {
		t.Fatalf("page=%+v more=%v err=%v", page, more, err)
	}
	page, more, total, err = s.SessionWork(ctx, "s", "turn2", 2)
	if err != nil || total != 3 || more || len(page) != 1 || page[0].Started.ID != "start1" || page[0].Ended != nil || len(page[0].Posts) != 1 {
		t.Fatalf("older=%+v more=%v err=%v", page, more, err)
	}
	if _, _, _, err := s.SessionWork(ctx, "s", "missing", 2); err != ErrInvalidWorkCursor {
		t.Fatalf("missing cursor error = %v", err)
	}
}
