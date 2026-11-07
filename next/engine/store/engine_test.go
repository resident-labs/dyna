package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestEngineRestartNotifiesOpenRoots(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name, build string
		clean       bool
		stopped     time.Time
		down        int64
	}{
		{name: "clean new build", build: "new", clean: true, stopped: time.UnixMilli(5000), down: 7500},
		{name: "clean same build", build: "old", clean: true, stopped: time.UnixMilli(5000), down: 7500},
		{name: "unclean", build: "new"},
		{name: "stop at epoch is clean", build: "new", clean: true, stopped: time.UnixMilli(0), down: 12500},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r1','r1',0,0,0),('r2','r2',0,0,0),('r3','r3',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root1','r1','root',NULL,'p','open','',0),('root2','r2','root',NULL,'p','open','',0),('worker','r1','worker','root1','p','open','',0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at,closed_at) VALUES('gone','r3','root',NULL,'p','closed','',0,1)`)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordEngineStart(ctx, "old", time.UnixMilli(-1000)); err != nil {
				t.Fatal(err)
			}
			var events, deliveries int
			if err := s.Read().QueryRow("SELECT (SELECT count(*) FROM events),(SELECT count(*) FROM deliveries)").Scan(&events, &deliveries); err != nil {
				t.Fatal(err)
			}
			if events != 0 || deliveries != 0 {
				t.Fatalf("first start sent %d events, %d deliveries", events, deliveries)
			}
			if test.clean {
				if err := s.RecordEngineStop(ctx, test.stopped); err != nil {
					t.Fatal(err)
				}
			}
			// The process state survives the connection that recorded it.
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			now := time.UnixMilli(12500)
			if err := s.RecordEngineStart(ctx, test.build, now); err != nil {
				t.Fatal(err)
			}
			want := contract.EngineRestarted{Build: test.build, PreviousBuild: "old", Clean: test.clean, DownMillis: test.down}
			for _, room := range []contract.RoomID{"r1", "r2", "r3"} {
				list, err := s.Events(ctx, room, 0, 100, []contract.EventType{"engine.restarted"})
				if err != nil {
					t.Fatal(err)
				}
				if room == "r3" {
					if len(list) != 0 {
						t.Fatalf("closed root received events: %v", list)
					}
					continue
				}
				if len(list) != 1 || list[0].Body != want || list[0].From.Kind != contract.PrincipalEngine || !list[0].Time.Equal(now) {
					t.Fatalf("room %s events = %+v, want one engine event with %+v", room, list, want)
				}
			}
			rows, err := s.Read().Query("SELECT to_id,surface,reply_kind,wake FROM deliveries ORDER BY to_id")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var recipients []string
			for rows.Next() {
				var to, surface, reply string
				var wake bool
				if err := rows.Scan(&to, &surface, &reply, &wake); err != nil {
					t.Fatal(err)
				}
				if surface != string(contract.SurfaceEngine) || reply != string(contract.ReplyNone) || wake {
					t.Fatalf("delivery to %s: surface=%s reply=%s wake=%v", to, surface, reply, wake)
				}
				recipients = append(recipients, to)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(recipients, []string{"root1", "root2"}) {
				t.Fatalf("deliveries to %v, want only the open roots", recipients)
			}
			var value string
			if err := s.Read().QueryRow("SELECT value FROM meta WHERE key=?", engineStateKey).Scan(&value); err != nil {
				t.Fatal(err)
			}
			var current engineState
			if err := json.Unmarshal([]byte(value), &current); err != nil {
				t.Fatal(err)
			}
			if current.Build != test.build || current.StartedAt != now.UnixMilli() || current.StoppedAt != nil {
				t.Fatalf("running state = %+v", current)
			}
		})
	}
}

func TestEngineRestartRollsBackStateWithNotice(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO sessions(id,room,name,parent,profile,state,goal,created_at) VALUES('root','r','root',NULL,'p','open','',0);
CREATE TRIGGER reject_notice BEFORE INSERT ON deliveries BEGIN SELECT RAISE(ABORT,'reject notice'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEngineStart(ctx, "old", time.UnixMilli(1000)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEngineStop(ctx, time.UnixMilli(2000)); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := s.Read().QueryRow("SELECT value FROM meta WHERE key=?", engineStateKey).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEngineStart(ctx, "new", time.UnixMilli(3000)); err == nil {
		t.Fatal("start accepted a failed delivery")
	}
	if err := s.Read().QueryRow("SELECT value FROM meta WHERE key=?", engineStateKey).Scan(&after); err != nil {
		t.Fatal(err)
	}
	var events, deliveries int
	if err := s.Read().QueryRow("SELECT (SELECT count(*) FROM events),(SELECT count(*) FROM deliveries)").Scan(&events, &deliveries); err != nil {
		t.Fatal(err)
	}
	if before != after || events != 0 || deliveries != 0 {
		t.Fatalf("failed transaction changed state: %s => %s, %d events, %d deliveries", before, after, events, deliveries)
	}
	if _, err := s.writer.Exec("DROP TRIGGER reject_notice"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEngineStart(ctx, "new", time.UnixMilli(3000)); err != nil {
		t.Fatal(err)
	}
	list, err := s.Events(ctx, "r", 0, 100, []contract.EventType{"engine.restarted"})
	want := contract.EngineRestarted{Build: "new", PreviousBuild: "old", Clean: true, DownMillis: 1000}
	if err != nil || len(list) != 1 || list[0].Body != want {
		t.Fatalf("retry events = %+v, %v; want one event with %+v", list, err, want)
	}
}
