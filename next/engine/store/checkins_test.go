package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestCheckinGoalItemCounts(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.CreateRoom(ctx, "r", "room", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('holder','r','holder','p','open','',1);
			INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('box','holder','','agent','ready','','',0,0);
			INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES('r',40,'holder','standing work','new input','active',1,1);
		`)
		if err != nil {
			return err
		}
		for _, tc := range []struct {
			state string
			total int
			open  int
		}{
			{"", 0, 0},
			{"done", 1, 0},
			{"dropped", 2, 0},
			{"open", 3, 1},
		} {
			if tc.state != "" {
				if _, err := tx.ExecContext(ctx, `INSERT INTO goal_items(room,goal,number,text,state,created_at,updated_at) VALUES('r',40,?,'step',?,1,1)`, tc.total, tc.state); err != nil {
					return err
				}
			}
			sessions, _, err := s.readCheckinRoom(ctx, tx, "r")
			if err != nil {
				return err
			}
			if len(sessions) != 1 || sessions[0].Goal != "g40" || sessions[0].Total != tc.total || sessions[0].Open != tc.open {
				t.Fatalf("after %q: %+v, want total %d open %d", tc.state, sessions, tc.total, tc.open)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
