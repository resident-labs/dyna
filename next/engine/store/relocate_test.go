package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
)

func relocationFixture(t *testing.T) (string, *Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.writer.Exec(`
INSERT INTO rooms(id,name,created_at,cpus,mem_bytes,only_hosts,root_approves) VALUES('r','room',1,2,100,'["old"]','["hostexec"]');
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,engine_host,gpus,agents,ci) VALUES
 ('old','pip@localhost','ready',32,100,200,3,1,'[{"index":0,"name":"GPU","memory":900}]',1,0),
 ('new','pip@100.106.214.124','ready',16,101,201,4,0,'[]',1,1),
 ('other','pip@other','unreachable',4,102,202,5,0,'[]',0,0);
INSERT INTO host_rooms VALUES('old','r'); INSERT INTO host_labels VALUES('old','gpu');
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','session','sol','open','keep history',1);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at,gpu_gib) VALUES('b','s','old','i','agent','stopped','','slice',1,1,1);
INSERT INTO events(id,at,room,type,from_kind,from_id,body) VALUES('ev',1,'r','post','owner','',X'010203');
INSERT INTO meta VALUES('test_secret','fake-secret');
INSERT INTO accounts VALUES('a','proxy-a','provider','account');
INSERT INTO grants(id,kind,room,to_kind,to_id,hosts,by_kind,by_id,created_at) VALUES('g','hostexec','r','session','s','["control-plane","old"]','owner','',1);
INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES
 ('historic','hostexec.run','r','session','s','auto','done','{}','{"room":"r","host":"control-plane","argv":["true"],"timeout":1000,"grant":"g"}',X'00',1,999),
 ('physical','hostexec.run','r','session','s','auto','running','{}','{"room":"r","host":"old","argv":["true"],"timeout":1000,"grant":"g"}',X'01',1,999);
INSERT INTO hostexec_runs(op,grant_id,host,scope,started_at,timeout_ms,ended_at) VALUES
 ('historic','g','control-plane','old-scope',1,999,2),('physical','g','old','physical-scope',1,999,NULL);
INSERT INTO subscription_runs VALUES('run','sub','r','other','agent',1);
`)
	if err != nil {
		t.Fatal(err)
	}
	return dir, s
}

// relocationSnapshot covers every table, ignoring only the three cells the command may change.
func relocationSnapshot(t *testing.T, s *Store, allowRelocation bool) map[string][]string {
	t.Helper()
	rows, err := s.Read().Query("SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, table := range tables {
		rows, err := s.Read().Query(fmt.Sprintf(`SELECT * FROM "%s"`, table))
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		out[table] = nil
		for rows.Next() {
			values := make([]any, len(cols))
			dest := make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			if allowRelocation && table == "hosts" {
				name := values[slices.Index(cols, "name")]
				if name == "old" || name == "new" {
					values[slices.Index(cols, "engine_host")] = nil
				}
				if name == "old" {
					values[slices.Index(cols, "ssh")] = nil
				}
			}
			b, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], string(b))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		slices.Sort(out[table])
	}
	return out
}

func relocationRequest(t *testing.T, from, endpoint, to string) EngineRelocation {
	t.Helper()
	r, err := NewEngineRelocation(from, endpoint, to)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRelocateEnginePreservesDataAndReplaysAndReverses(t *testing.T) {
	dir, s := relocationFixture(t)
	before := relocationSnapshot(t, s, true)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	request := relocationRequest(t, "old", "pip@100.122.131.34", "new")
	for range 2 {
		if err := RelocateEngine(context.Background(), dir, request); err != nil {
			t.Fatal(err)
		}
		s, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got := relocationSnapshot(t, s, true); !reflect.DeepEqual(got, before) {
			t.Fatal("relocation changed data outside the three permitted cells")
		}
		hosts, err := s.Hosts(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hosts {
			if h.EngineHost == nil || *h.EngineHost != (h.Name == "new") {
				t.Fatalf("identity: %+v", h)
			}
			if h.Name == "old" && h.SSH != "pip@100.122.131.34" {
				t.Fatalf("old address: %+v", h)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := RelocateEngine(context.Background(), dir, relocationRequest(t, "new", "pip@100.106.214.124", "old")); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := relocationSnapshot(t, s, true); !reflect.DeepEqual(got, before) {
		t.Fatal("reverse changed unrelated data")
	}
	hosts, err := s.Hosts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		if h.EngineHost == nil || *h.EngineHost != (h.Name == "old") {
			t.Fatalf("reverse identity: %+v", h)
		}
	}
}

func TestRelocateEngineValidatesBoundary(t *testing.T) {
	for _, c := range [][3]string{
		{"old", "pip@remote", "old"}, {"", "pip@remote", "new"}, {"old", "pip@remote", "../new"},
		{"old", "pip@localhost", "new"}, {"old", "pip@LOCALHOST.", "new"}, {"old", "pip@x.localhost", "new"},
		{"old", "pip@127.1", "new"}, {"old", "pip@2130706433", "new"}, {"old", "pip@0177.0.0.1", "new"}, {"old", "pip@0x7f000001", "new"},
		{"old", "pip@127.0.0.1", "new"}, {"old", "pip@127.10.2.3", "new"}, {"old", "pip@0.0.0.0", "new"},
		{"old", "pip@::1", "new"}, {"old", "-oProxyCommand=x", "new"}, {"old", "pip@remote;bad", "new"},
		{"old", "pip@remote:22", "new"}, {"old", "ssh://pip@remote", "new"}, {"old", "pip@remote\n", "new"},
	} {
		t.Run(strings.Join(c[:], "_"), func(t *testing.T) {
			if _, err := NewEngineRelocation(c[0], c[1], c[2]); err == nil {
				t.Fatal("accepted invalid relocation")
			}
		})
	}
}

func TestRelocateEngineRefusesWithoutWrites(t *testing.T) {
	for _, kind := range []string{"live", "absent-from", "absent-to", "old-schema", "new-schema", "active-former-box", "active-former-run", "active-local-run", "pending-local-op", "approved-local-op", "running-local-op", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			dir, s := relocationFixture(t)
			from, to := "old", "new"
			switch kind {
			case "absent-from":
				from = "absent"
			case "absent-to":
				to = "absent"
			case "old-schema":
				_, err := s.writer.Exec("UPDATE meta SET value=? WHERE key='schema_version'", schemaVersion-1)
				if err != nil {
					t.Fatal(err)
				}
			case "new-schema":
				_, err := s.writer.Exec("UPDATE meta SET value=? WHERE key='schema_version'", schemaVersion+1)
				if err != nil {
					t.Fatal(err)
				}
			case "active-former-box":
				_, err := s.writer.Exec("UPDATE boxes SET state='ready' WHERE id='b'")
				if err != nil {
					t.Fatal(err)
				}
			case "active-former-run":
				_, err := s.writer.Exec("UPDATE subscription_runs SET host='old'")
				if err != nil {
					t.Fatal(err)
				}
			case "active-local-run":
				_, err := s.writer.Exec("UPDATE hostexec_runs SET ended_at=NULL WHERE op='historic'")
				if err != nil {
					t.Fatal(err)
				}
			case "pending-local-op", "approved-local-op", "running-local-op":
				state := strings.TrimSuffix(kind, "-local-op")
				_, err := s.writer.Exec("UPDATE operations SET state=? WHERE id='historic'", state)
				if err != nil {
					t.Fatal(err)
				}
			case "rollback":
				_, err := s.writer.Exec("CREATE TRIGGER refuse_destination BEFORE UPDATE ON hosts WHEN NEW.name='new' BEGIN SELECT RAISE(ABORT,'injected update failure'); END")
				if err != nil {
					t.Fatal(err)
				}
			}
			before := relocationSnapshot(t, s, false)
			if kind != "live" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			err := RelocateEngine(context.Background(), dir, relocationRequest(t, from, "pip@100.122.131.34", to))
			if err == nil {
				t.Fatal("wanted relocation refusal")
			}
			if kind == "rollback" && !strings.Contains(err.Error(), "injected update failure") {
				t.Fatalf("did not exercise second-update rollback: %v", err)
			}
			if kind == "live" {
				if got := relocationSnapshot(t, s, false); !reflect.DeepEqual(got, before) {
					t.Fatal("live store changed")
				}
				return
			}
			// Read without upgrading; even the schema-version sentinel must be unchanged.
			db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "store.db")+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			got := relocationSnapshot(t, &Store{read: db}, false)
			if !reflect.DeepEqual(got, before) {
				t.Fatal("refusal changed persistent data")
			}
		})
	}
}

func TestRelocateEngineMissingStoreNeverInitializes(t *testing.T) {
	dir := t.TempDir()
	request := relocationRequest(t, "old", "pip@remote", "new")
	if err := RelocateEngine(context.Background(), dir, request); err == nil {
		t.Fatal("accepted missing store")
	}
	if _, err := os.Stat((box.Layout{Data: dir}).Store()); !os.IsNotExist(err) {
		t.Fatalf("created missing store: %v", err)
	}
	if err := RelocateEngine(context.Background(), filepath.Join(dir, "missing"), request); err == nil {
		t.Fatal("accepted missing data folder")
	}
}
