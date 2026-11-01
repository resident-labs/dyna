package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

var errCrash = errors.New("crash")

// A box can leave a read-only folder in its room's shared folder, which the engine's own user cannot remove.
func TestRemoveRoomDataClearsWhatABoxLockedUp(t *testing.T) {
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "podman"), []byte("#!/bin/sh\nchmod -R u+w \"$5\" && rm -rf \"$5\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+":"+os.Getenv("PATH"))
	layout := box.Layout{Data: t.TempDir()}
	s := &Service{layout: layout}
	locked := filepath.Join(layout.Shared("room_1"), "locked")
	if err := os.MkdirAll(locked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0700) })
	if err := s.RemoveRoomData("room_1"); err != nil {
		t.Fatal(err)
	}
	dir, _ := layout.Room("room_1")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the room's folder is still there: %v", err)
	}
}

// fakeRoomHosts records removals in order. Err fails a removal; crash does the removal and then fails, as a crash after it would.
type fakeRoomHosts struct {
	mu         sync.Mutex
	err, crash error
	boxErr     error
	removed    []string
	forgotten  []contract.BoxID
}

// failBoxes sets the failure of a box removal.
func (h *fakeRoomHosts) failBoxes(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.boxErr = err
}

// deleteBoxes removes boxes unless the test made that fail.
type deleteBoxes struct {
	fakeBoxes
	hosts *fakeRoomHosts
}

func (b deleteBoxes) Remove(context.Context, contract.BoxID) error {
	b.hosts.mu.Lock()
	defer b.hosts.mu.Unlock()
	return b.hosts.boxErr
}

func (h *fakeRoomHosts) record(name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	h.removed = append(h.removed, name)
	return h.crash
}
func (h *fakeRoomHosts) RemoveRoom(_ context.Context, host contract.HostName, room contract.RoomID) error {
	return h.record(fmt.Sprintf("room %s %s", host, room))
}
func (h *fakeRoomHosts) RemoveLeftoverBox(_ context.Context, host contract.HostName, id contract.BoxID, slice string) error {
	return h.record(fmt.Sprintf("box %s %s %s", host, id, slice))
}
func (h *fakeRoomHosts) ForgetBox(id contract.BoxID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.forgotten = append(h.forgotten, id)
	return nil
}
func (h *fakeRoomHosts) seen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.removed...)
}

// crashingData fails after the folder is gone, as a crash before the rows would.
type crashingData struct {
	lifecycle.RoomData
	crash error
}

func (d crashingData) RemoveRoomData(room contract.RoomID) error {
	if err := d.RoomData.RemoveRoomData(room); err != nil {
		return err
	}
	return d.crash
}

// deleteRig runs a service over a store that a test can restart, as an engine restart would.
type deleteRig struct {
	dir   string
	st    *store.Store
	s     *Service
	hosts *fakeRoomHosts
}

var deleteOwner = contract.Principal{Kind: contract.PrincipalOwner}

func newDeleteRig(t *testing.T) (*deleteRig, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &deleteRig{dir: t.TempDir(), hosts: &fakeRoomHosts{}}
	r.open(t)
	t.Cleanup(func() {
		r.s.reconciler.Wait()
		if err := r.st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.st.AddHost(ctx, box.Host{Name: "host", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: time.Unix(1000, 0)}); err != nil {
		t.Fatal(err)
	}
	return r, ctx
}

func (r *deleteRig) open(t *testing.T) {
	t.Helper()
	st, err := store.Open(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	r.st = st
	r.s = New(st, deleteBoxes{hosts: r.hosts}, fakeProfiles{}, func() time.Time { return time.Unix(1000, 0) })
	resetLifecycle(r.s)
	r.s.SetLayout(box.Layout{Data: r.dir, HostData: "/host"})
	r.s.reconciler.SetRoomHosts(r.hosts)
}

func (r *deleteRig) restart(t *testing.T) {
	t.Helper()
	r.s.reconciler.Wait()
	if err := r.st.Close(); err != nil {
		t.Fatal(err)
	}
	r.open(t)
}

// room makes a room with a root and a worker, placed on the host.
func (r *deleteRig) room(t *testing.T, ctx context.Context, name string) (contract.RoomID, contract.SessionID, contract.SessionID) {
	t.Helper()
	created, e := r.s.RoomCreate(ctx, deleteOwner, contract.RoomCreateRequest{Name: name, RootProfile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, r.s, ctx)
	worker, e := r.s.SessionStart(ctx, deleteOwner, contract.SessionStartRequest{Room: created.Room, Name: "worker", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, r.s, ctx)
	var root contract.SessionID
	if err := r.st.Read().QueryRowContext(ctx, "SELECT parent FROM sessions WHERE id=?", worker.Session).Scan(&root); err != nil {
		t.Fatal(err)
	}
	return created.Room, root, worker.Session
}

func (r *deleteRig) upload(t *testing.T, room contract.RoomID, body string) contract.Attachment {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/files?name=note.txt&room="+string(room), bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	r.s.Files(deleteOwner, "").ServeHTTP(rec, req)
	var file contract.Attachment
	if rec.Code != 200 || json.NewDecoder(rec.Body).Decode(&file) != nil {
		t.Fatalf("upload: %d", rec.Code)
	}
	return file
}

func (r *deleteRig) read(caller contract.Principal, room contract.RoomID, hash string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.s.Files(caller, room).ServeHTTP(rec, httptest.NewRequest("GET", "/v1/files/"+hash, nil))
	return rec
}

func (r *deleteRig) count(t *testing.T, ctx context.Context, query string, args ...any) int {
	t.Helper()
	var n int
	if err := r.st.Read().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (r *deleteRig) delete(t *testing.T, ctx context.Context, room contract.RoomID) contract.RoomDeleteResponse {
	t.Helper()
	response, e := r.s.RoomDelete(ctx, deleteOwner, contract.RoomDeleteRequest{Room: room, Discard: true})
	if e != nil {
		t.Fatal(e)
	}
	return response
}

// settle runs passes until the room's rows are gone.
func (r *deleteRig) settle(t *testing.T, ctx context.Context, room contract.RoomID) {
	t.Helper()
	for range 20 {
		passLifecycle(t, r.s, ctx)
		if r.count(t, ctx, "SELECT count(*) FROM rooms WHERE id=?", room) == 0 {
			return
		}
	}
	t.Fatal("room was not deleted")
}

// assertGone checks every table with a room or session column, so a table added later without a place in deleteRoom fails here.
func (r *deleteRig) assertGone(t *testing.T, ctx context.Context, room contract.RoomID, sessions ...contract.SessionID) {
	t.Helper()
	columns, err := r.st.Read().QueryContext(ctx, `SELECT m.name,p.name FROM sqlite_schema m JOIN pragma_table_info(m.name) p WHERE m.type='table' AND p.name IN ('room','session') AND m.sql NOT LIKE 'CREATE VIRTUAL%'`)
	if err != nil {
		t.Fatal(err)
	}
	type column struct{ table, name string }
	var found []column
	for columns.Next() {
		var c column
		if err := columns.Scan(&c.table, &c.name); err != nil {
			t.Fatal(err)
		}
		found = append(found, c)
	}
	columns.Close()
	if len(found) < 10 {
		t.Fatalf("schema walk found only %v", found)
	}
	for _, c := range found {
		if c.table == "room_leftovers" {
			continue
		}
		value := []any{string(room)}
		if c.name == "session" {
			value = value[:0]
			for _, id := range sessions {
				value = append(value, string(id))
			}
		}
		query := fmt.Sprintf("SELECT count(*) FROM %s WHERE %s IN (?%s)", c.table, c.name, strings.Repeat(",?", len(value)-1))
		if c.table == "archives" {
			query += " AND ref=''"
		}
		if n := r.count(t, ctx, query, value...); n != 0 {
			t.Errorf("%s.%s keeps %d rows", c.table, c.name, n)
		}
	}
	for _, query := range []string{
		"SELECT count(*) FROM rooms WHERE id=?1",
		"SELECT count(*) FROM repos WHERE default_room=?1",
		"SELECT count(*) FROM events WHERE room=?1",
	} {
		if n := r.count(t, ctx, query, room); n != 0 {
			t.Errorf("%s: %d rows", query, n)
		}
	}
	for _, id := range sessions {
		for _, query := range []string{
			"SELECT count(*) FROM deliveries WHERE to_id=?1",
			"SELECT count(*) FROM operations WHERE by_id=?1",
			"SELECT count(*) FROM client_replays WHERE caller_id=?1 OR ref=?1",
			"SELECT count(*) FROM grants WHERE to_id=?1",
		} {
			if n := r.count(t, ctx, query, id); n != 0 {
				t.Errorf("%s: %d rows", query, n)
			}
		}
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM pragma_foreign_key_check"); n != 0 {
		t.Errorf("%d foreign key violations", n)
	}
	dir, err := r.s.layout.Room(room)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("room folder: %v", err)
	}
}

// fillRoom gives the room a row in each table a delete must reach, including deliveries that cross into the other room both ways.
func (r *deleteRig) fillRoom(t *testing.T, ctx context.Context, room contract.RoomID, worker contract.SessionID, other contract.RoomID) {
	t.Helper()
	r.s.SetRepositories(&fakeRepos{})
	if e := r.s.store.AddRoomRepo(ctx, room, "owner/only", true); e != nil {
		t.Fatal(e)
	}
	if e := r.s.store.AddRoomRepo(ctx, room, "owner/both", false); e != nil {
		t.Fatal(e)
	}
	if e := r.s.store.AddRoomRepo(ctx, other, "owner/both", false); e != nil {
		t.Fatal(e)
	}
	file := r.upload(t, room, "kept in the room")
	if _, e := r.s.Post(ctx, deleteOwner, contract.PostRequest{Room: room, Text: "findme", Attachments: []contract.Attachment{file}}); e != nil {
		t.Fatal(e)
	}
	visual := r.upload(t, room, "<title>Card</title>")
	visual.Media, visual.Visual = "text/html", &contract.VisualCard{Height: 120}
	if _, e := r.s.Post(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.PostRequest{Room: room, Text: "card", Attachments: []contract.Attachment{visual}}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.Post(ctx, deleteOwner, contract.PostRequest{Room: other, Text: "to the room", Mentions: []string{"@room:worker"}}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.Post(ctx, deleteOwner, contract.PostRequest{Room: room, Text: "from the room", Mentions: []string{"@other:worker"}}); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.TranscriptAppend(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: worker}, contract.TranscriptAppendRequest{Path: "a.jsonl", Bytes: []byte("line\n")}); e != nil {
		t.Fatal(e)
	}
	before, e := r.s.InstructionsGet(ctx, deleteOwner, contract.InstructionsGetRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.InstructionsSet(ctx, deleteOwner, contract.InstructionsSetRequest{Room: room, Base: before.Hash, Text: "rules"}); e != nil {
		t.Fatal(e)
	}
	if _, err := r.st.SetConversationRead(ctx, room, "feed", 1); err != nil {
		t.Fatal(err)
	}
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		for _, statement := range []string{
			`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_delete',?2,0,1,'completed','',0,0,0,0,0)`,
			`INSERT INTO transcript_turns(session,turn,path,start) VALUES(?2,'turn_delete','a.jsonl',0)`,
			`INSERT INTO shell_jobs(session,job) VALUES(?2,'job_1')`,
			`INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('owner/only',7,?2,'','')`,
			`INSERT INTO issue_owners(repo,number,session,url,title,author,state) VALUES('owner/only',8,?2,'','','','open')`,
			`INSERT INTO repo_ci_hosts(repo,host,runners,size) VALUES('owner/only','host',1,'large'),('owner/both','host',1,'large')`,
			`INSERT INTO archives(session,repo,ref,commit_sha,created_at,expires_at) VALUES(?2,'owner/only','','',0,99999999)`,
			`INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('op_delete','hostexec.run',?1,'session',?2,'auto','done','{}','{}',x'01',0,0)`,
			`INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('op_global','accounts.action',NULL,'session',?2,'owner','done','{}','{}',x'02',0,0)`,
			`INSERT INTO hostexec_runs(op,grant_id,host,scope,started_at,timeout_ms,ended_at) SELECT 'op_delete',id,'host','scope',0,0,1 FROM grants WHERE room=?1 AND kind='hostexec'`,
			`INSERT INTO questions(id,room,session,text,state,asked_at) VALUES('q_delete',?1,?2,'why?','pending',0)`,
			`INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES('question.ask','owner','','key3','q_delete',0)`,
			`INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES('session.start','owner','','key1',?2,0)`,
			`INSERT INTO client_replays(method,caller_kind,caller_id,client_id,ref,created_at) VALUES('op.submit','session',?2,'key2','op_delete',0)`,
			`INSERT INTO room_shared(room,bytes,measured_at) VALUES(?1,1,0)`,
			`INSERT INTO host_rooms(host,room) VALUES('host',?1),('host',?3)`,
		} {
			if _, err := tx.ExecContext(ctx, statement, room, worker, other); err != nil {
				return fmt.Errorf("%s: %w", statement, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoomDeleteRemovesEveryRowAndFolder(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, root, worker := r.room(t, ctx, "room")
	other, _, otherWorker := r.room(t, ctx, "other")
	r.fillRoom(t, ctx, room, worker, other)
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		for _, statement := range []string{
			`INSERT INTO accounts(id,proxy_id,provider,label) VALUES('open','p1','claude','open'),('both','p2','claude','both')`,
			`INSERT INTO account_rooms(account,room) VALUES('both',?1),('both',?2)`,
		} {
			if _, err := tx.ExecContext(ctx, statement, room, other); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM post_search WHERE post_search MATCH 'findme'"); n != 1 {
		t.Fatalf("post_search before: %d", n)
	}
	sessions := r.count(t, ctx, "SELECT count(*) FROM session_search")
	crossed := r.count(t, ctx, "SELECT count(*) FROM deliveries WHERE to_id=?", otherWorker)
	r.delete(t, ctx, room)
	r.settle(t, ctx, room)
	r.assertGone(t, ctx, room, root, worker)

	if n := r.count(t, ctx, "SELECT count(*) FROM post_search WHERE post_search MATCH 'findme'"); n != 0 {
		t.Errorf("post_search keeps %d rows", n)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM session_search"); n != sessions-2 {
		t.Errorf("session_search has %d rows of %d", n, sessions)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM accounts"); n != 2 {
		t.Errorf("accounts: %d", n)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM account_rooms WHERE account='both' AND room=?", other); n != 1 {
		t.Errorf("the other room's link: %d", n)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM host_rooms WHERE host='host' AND room=?", other); n != 1 {
		t.Errorf("the host's other room: %d", n)
	}
	var defaultRoom contract.RoomID
	if err := r.st.Read().QueryRowContext(ctx, "SELECT default_room FROM repos WHERE repo='owner/both'").Scan(&defaultRoom); err != nil || defaultRoom != other {
		t.Errorf("default room %s: %v", defaultRoom, err)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM repo_ci_hosts WHERE repo='owner/both'"); n != 1 {
		t.Errorf("CI hosts of a repository that stays: %d", n)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM repo_ci_hosts WHERE repo='owner/only'"); n != 0 {
		t.Errorf("CI hosts of a repository that went: %d", n)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM repos WHERE repo='owner/only'"); n != 0 {
		t.Errorf("repo only this room listed: %d", n)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM sessions WHERE room=?", other); n != 2 {
		t.Errorf("other room's sessions: %d", n)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM deliveries WHERE to_id=?", otherWorker); n != crossed-1 {
		t.Errorf("deliveries to the other room: %d of %d, after losing the deleted room's event", n, crossed)
	}
	if got := r.hosts.seen(); len(got) != 1 || got[0] != "room host "+string(room) {
		t.Errorf("host removals: %v", got)
	}
	if _, e := r.s.RoomList(ctx, deleteOwner, contract.RoomListRequest{}); e != nil {
		t.Fatal(e)
	}
}

// limitRoom adds an account and a host whose limits hold exactly these rooms.
func (r *deleteRig) limitRoom(t *testing.T, ctx context.Context, name string, rooms ...contract.RoomID) {
	t.Helper()
	if err := r.st.AddHost(ctx, box.Host{Name: contract.HostName(name), State: box.HostReady, CheckedAt: time.Unix(1000, 0), Rooms: rooms}); err != nil {
		t.Fatal(err)
	}
	if err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,proxy_id,provider,label) VALUES(?1,?1,'claude',?1)", name)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetAccountRooms(ctx, contract.AccountID(name), rooms); err != nil {
		t.Fatal(err)
	}
}

func (r *deleteRig) refused(t *testing.T, ctx context.Context, room contract.RoomID, accounts []contract.AccountID, hosts []contract.HostName, names ...string) {
	t.Helper()
	_, e := r.s.RoomDelete(ctx, deleteOwner, contract.RoomDeleteRequest{Room: room, Discard: true})
	if e == nil || e.Code != contract.ErrRoomLimited || e.Limited == nil {
		t.Fatalf("got %+v", e)
	}
	if !slices.Equal(e.Limited.Accounts, accounts) || !slices.Equal(e.Limited.Hosts, hosts) {
		t.Fatalf("limited: %+v", e.Limited)
	}
	for _, name := range names {
		if !strings.Contains(e.Message, name) {
			t.Fatalf("%q does not name %s", e.Message, name)
		}
	}
}

func TestRoomDeleteRefusesToWidenALimit(t *testing.T) {
	account := []contract.AccountID{"limited"}
	host := []contract.HostName{"limited"}
	for _, tc := range []struct {
		name     string
		accounts []contract.AccountID
		hosts    []contract.HostName
	}{{"account only", account, nil}, {"host only", nil, host}, {"both", account, host}} {
		t.Run(tc.name, func(t *testing.T) {
			r, ctx := newDeleteRig(t)
			room, _, worker := r.room(t, ctx, "room")
			other, _, _ := r.room(t, ctx, "other")
			r.limitRoom(t, ctx, "limited", room, other)
			// Each limit starts safe; the case narrows one or both to this room alone.
			if tc.accounts != nil {
				if err := r.st.SetAccountRooms(ctx, "limited", []contract.RoomID{room}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.hosts != nil {
				if err := r.st.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "DELETE FROM host_rooms WHERE host='limited' AND room=?", other)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			var names []string
			if tc.accounts != nil {
				names = append(names, "account limited")
			}
			if tc.hosts != nil {
				names = append(names, "host limited")
			}
			events := r.count(t, ctx, "SELECT count(*) FROM events WHERE room=?", room)
			r.refused(t, ctx, room, tc.accounts, tc.hosts, names...)
			if n := r.count(t, ctx, "SELECT count(*) FROM rooms WHERE id=? AND state='open'", room); n != 1 {
				t.Fatal("a refused delete changed the room")
			}
			if n := r.count(t, ctx, "SELECT count(*) FROM events WHERE room=?", room); n != events {
				t.Fatal("a refused delete left an event")
			}
			if n := r.count(t, ctx, "SELECT count(*) FROM boxes WHERE session=? AND want='discard'", worker); n != 0 {
				t.Fatal("a refused delete closed a session")
			}
			// After the owner changes the limit, the delete runs and the limit keeps its other room.
			if err := r.st.SetAccountRooms(ctx, "limited", []contract.RoomID{other}); err != nil {
				t.Fatal(err)
			}
			if err := r.st.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE OR IGNORE host_rooms SET room=?1 WHERE host='limited'", other)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			r.delete(t, ctx, room)
			r.settle(t, ctx, room)
			if n := r.count(t, ctx, "SELECT count(*) FROM account_rooms WHERE account='limited' AND room=?", other); n != 1 {
				t.Fatalf("account limit: %d", n)
			}
			if n := r.count(t, ctx, "SELECT count(*) FROM host_rooms WHERE host='limited' AND room=?", other); n != 1 {
				t.Fatalf("host limit: %d", n)
			}
		})
	}
}

func TestRoomDeleteDropsThisRoomFromASharedLimit(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, _ := r.room(t, ctx, "room")
	other, _, _ := r.room(t, ctx, "other")
	r.limitRoom(t, ctx, "shared", room, other)
	r.delete(t, ctx, room)
	r.settle(t, ctx, room)
	for table, query := range map[string]string{
		"account_rooms": "SELECT count(*) FROM account_rooms WHERE account='shared'",
		"host_rooms":    "SELECT count(*) FROM host_rooms WHERE host='shared'",
	} {
		if n := r.count(t, ctx, query); n != 1 {
			t.Errorf("%s: %d rows, want the other room's", table, n)
		}
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM account_rooms WHERE room=?1", room); n != 0 {
		t.Errorf("links kept: %d", n)
	}
}

// A deleting room still has its links, so counting them would let two deletes empty one limit.
func TestRoomDeleteDoesNotCountADeletingRoomAsCover(t *testing.T) {
	r, ctx := newDeleteRig(t)
	roomA, _, _ := r.room(t, ctx, "a")
	roomB, _, _ := r.room(t, ctx, "b")
	r.limitRoom(t, ctx, "shared", roomA, roomB)
	r.hosts.err = errors.New("hold the room")
	r.delete(t, ctx, roomA)
	r.refused(t, ctx, roomB, []contract.AccountID{"shared"}, []contract.HostName{"shared"})
	r.hosts.err = nil
	r.settle(t, ctx, roomA)
	r.refused(t, ctx, roomB, []contract.AccountID{"shared"}, []contract.HostName{"shared"})
}

// The check runs in the write that sets deleting. This limit commits while the delete waits for the writer, after any read the
// delete could have made outside its transaction, so only a check inside it sees the limit.
func TestRoomDeleteRechecksLimitsInItsTransaction(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, _ := r.room(t, ctx, "room")
	if err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO accounts(id,proxy_id,provider,label) VALUES('late','late','claude','late')")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	result := make(chan *contract.Error, 1)
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		go func() {
			_, e := r.s.RoomDelete(ctx, deleteOwner, contract.RoomDeleteRequest{Room: room, Discard: true})
			result <- e
		}()
		time.Sleep(100 * time.Millisecond)
		_, err := tx.ExecContext(ctx, "INSERT INTO account_rooms(account,room) VALUES('late',?)", room)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	e := <-result
	if e == nil || e.Code != contract.ErrRoomLimited || !strings.Contains(e.Message, "account late") {
		t.Fatalf("got %+v", e)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM rooms WHERE id=? AND state='open'", room); n != 1 {
		t.Fatal("the room went into deleting")
	}
}

// The other order: once the room is deleting, no limit can come to depend on it.
func TestLimitsRefuseARoomBeingDeleted(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, _ := r.room(t, ctx, "room")
	other, _, _ := r.room(t, ctx, "other")
	r.limitRoom(t, ctx, "shared", room, other)
	r.hosts.err = errors.New("hold the room")
	r.delete(t, ctx, room)
	if err := r.st.SetAccountRooms(ctx, "shared", []contract.RoomID{room}); !errors.Is(err, store.ErrRoomDeleting) {
		t.Fatalf("account: %v", err)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM account_rooms WHERE account='shared'"); n != 2 {
		t.Fatalf("a refused change lost links: %d", n)
	}
	if err := r.st.AddHost(ctx, box.Host{Name: "new", State: box.HostReady, CheckedAt: time.Unix(1000, 0), Rooms: []contract.RoomID{room}}); !errors.Is(err, store.ErrRoomDeleting) {
		t.Fatalf("host: %v", err)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM hosts WHERE name='new'"); n != 0 {
		t.Fatal("a refused host stayed")
	}
	r.hosts.err = nil
	r.settle(t, ctx, room)
}

func TestRoomDeleteKeepsABlobAnotherRoomUses(t *testing.T) {
	r, ctx := newDeleteRig(t)
	roomA, _, _ := r.room(t, ctx, "a")
	roomB, _, workerB := r.room(t, ctx, "b")
	file := r.upload(t, roomA, "shared bytes")
	post := func(room contract.RoomID) {
		t.Helper()
		if _, e := r.s.Post(ctx, deleteOwner, contract.PostRequest{Room: room, Text: "file", Attachments: []contract.Attachment{file}}); e != nil {
			t.Fatal(e)
		}
	}
	post(roomA)
	post(roomB) // The owner forwards it.
	post(roomA) // The newest use is in the room that goes first.
	first, err := os.Stat(r.s.layout.File(roomA, file.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(r.s.layout.File(roomB, file.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(first, second) {
		t.Fatal("a forward copied the blob instead of linking it")
	}
	r.delete(t, ctx, roomA)
	// While the room goes, its folder may already be gone. The owner still reads the other room's link.
	if err := r.s.RemoveRoomData(roomA); err != nil {
		t.Fatal(err)
	}
	if rec := r.read(deleteOwner, "", file.SHA256); rec.Code != http.StatusOK || rec.Body.String() != "shared bytes" {
		t.Fatalf("while deleting: %d %s", rec.Code, rec.Body)
	}
	r.settle(t, ctx, roomA)
	b := contract.Principal{Kind: contract.PrincipalSession, Session: workerB}
	for _, tc := range []struct {
		caller contract.Principal
		room   contract.RoomID
	}{{deleteOwner, ""}, {b, roomB}} {
		if rec := r.read(tc.caller, tc.room, file.SHA256); rec.Code != http.StatusOK || rec.Body.String() != "shared bytes" {
			t.Fatalf("after delete: %d %s", rec.Code, rec.Body)
		}
	}
	if _, e := r.s.Post(ctx, b, contract.PostRequest{Room: roomB, Text: "again", Attachments: []contract.Attachment{file}}); e != nil {
		t.Fatalf("a session reuses its room's blob: %v", e)
	}
}

// Each case stops the engine at one step; a restart must finish the deletion from there.
func TestRoomDeleteResumesAfterACrashAtEachStep(t *testing.T) {
	for _, step := range []string{"marked", "closing", "hosts", "files"} {
		t.Run(step, func(t *testing.T) {
			r, ctx := newDeleteRig(t)
			room, root, worker := r.room(t, ctx, "room")
			other, _, _ := r.room(t, ctx, "other")
			r.fillRoom(t, ctx, room, worker, other)
			r.delete(t, ctx, room)
			switch step {
			case "marked":
			case "closing":
				passLifecycle(t, r.s, ctx) // Boxes go, but the sessions stay open.
				if n := r.count(t, ctx, "SELECT count(*) FROM sessions WHERE room=? AND closed_at IS NULL", room); n == 0 {
					t.Fatal("close finished in one pass")
				}
			case "hosts":
				r.hosts.crash = errCrash
			case "files":
				r.s.reconciler.SetRoomData(crashingData{RoomData: r.s, crash: errCrash})
			}
			if step == "hosts" || step == "files" {
				for range 6 {
					passLifecycle(t, r.s, ctx)
				}
				var state, reason string
				if err := r.st.Read().QueryRowContext(ctx, "SELECT state,reason FROM rooms WHERE id=?", room).Scan(&state, &reason); err != nil {
					t.Fatal(err)
				}
				if state != "deleting" || !strings.Contains(reason, "crash") {
					t.Fatalf("state %q reason %q", state, reason)
				}
			}
			if n := r.count(t, ctx, "SELECT count(*) FROM rooms WHERE id=? AND state='deleting'", room); n != 1 {
				t.Fatalf("intent lost before the crash: %d", n)
			}
			r.hosts.crash = nil
			r.restart(t)
			if _, e := r.s.SessionStart(ctx, deleteOwner, contract.SessionStartRequest{Room: room, Name: "late", Profile: "test", Parent: root}); e == nil {
				t.Fatal("a restarted engine took a session in a deleting room")
			}
			r.settle(t, ctx, room)
			r.assertGone(t, ctx, room, root, worker)
			if err := r.st.DeleteRoom(ctx, room); err != nil {
				t.Fatalf("a second run changes nothing: %v", err)
			}
		})
	}
}

func TestRoomDeleteDoesNotWaitForAnOfflineHost(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, root, worker := r.room(t, ctx, "room")
	boxes := map[contract.BoxID]bool{}
	for _, id := range []contract.SessionID{root, worker} {
		session, err := r.st.Session(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		boxes[session.Box] = true
	}
	offline := func(reachable bool) {
		t.Helper()
		if err := r.st.RecordHostHeartbeat(ctx, "host", reachable, nil, 0, time.Unix(1000, 0)); err != nil {
			t.Fatal(err)
		}
	}
	offline(false)
	r.delete(t, ctx, room)
	r.settle(t, ctx, room)
	r.assertGone(t, ctx, room, root, worker)
	if got := r.hosts.seen(); len(got) != 0 {
		t.Fatalf("reached an offline host: %v", got)
	}
	if len(r.hosts.forgotten) != 2 {
		t.Fatalf("engine sockets kept: %v", r.hosts.forgotten)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM room_leftovers WHERE host='host'"); n != 3 {
		t.Fatalf("debts: %d, want two boxes and the folder", n)
	}
	list, e := r.s.HostList(ctx, deleteOwner, contract.HostListRequest{})
	if e != nil || len(list.Hosts) != 2 || list.Hosts[1].Leftovers != 3 {
		t.Fatalf("not shown: %+v %v", list, e)
	}
	// The debt survives a restart, and a failed repayment keeps it.
	r.restart(t)
	offline(true)
	r.hosts.err = errors.New("no such host")
	passLifecycle(t, r.s, ctx)
	if n := r.count(t, ctx, "SELECT count(*) FROM room_leftovers"); n != 3 {
		t.Fatalf("debts after a failed repayment: %d", n)
	}
	r.hosts.err = nil
	passLifecycle(t, r.s, ctx)
	if n := r.count(t, ctx, "SELECT count(*) FROM room_leftovers"); n != 0 {
		t.Fatalf("debts left: %d", n)
	}
	got := r.hosts.seen()
	if len(got) != 3 || !strings.HasPrefix(got[0], "box host") || !strings.HasPrefix(got[1], "box host") || got[2] != "room host "+string(room) {
		t.Fatalf("a box mounts the folder, so boxes go first: %v", got)
	}
	for _, removal := range got[:2] {
		id := contract.BoxID(strings.Fields(removal)[2])
		if !boxes[id] || !strings.HasSuffix(removal, ".slice") {
			t.Fatalf("removal %q of %v", removal, boxes)
		}
	}
}

func TestRoomDeleteShowsWhatHoldsIt(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, _ := r.room(t, ctx, "room")
	r.hosts.err = errors.New("permission denied")
	r.delete(t, ctx, room)
	for range 6 {
		passLifecycle(t, r.s, ctx)
	}
	list, e := r.s.RoomList(ctx, deleteOwner, contract.RoomListRequest{})
	if e != nil || len(list.Rooms) != 1 || !list.Rooms[0].Deleting || !strings.Contains(list.Rooms[0].DeleteReason, "permission denied") {
		t.Fatalf("%+v %v", list, e)
	}
	_, e = r.s.RoomDelete(ctx, deleteOwner, contract.RoomDeleteRequest{Room: room, Discard: true})
	if e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "permission denied") {
		t.Fatalf("second delete: %v", e)
	}
	r.hosts.err = nil
	r.settle(t, ctx, room)
}

// A box that a ready host cannot remove holds the room as a folder would, and says so in the same place.
func TestRoomDeleteShowsABoxThatHoldsIt(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, _ := r.room(t, ctx, "room")
	r.hosts.failBoxes(errors.New("container in use"))
	r.delete(t, ctx, room)
	for range 6 {
		passLifecycle(t, r.s, ctx)
	}
	list, e := r.s.RoomList(ctx, deleteOwner, contract.RoomListRequest{})
	if e != nil || len(list.Rooms) != 1 || !strings.Contains(list.Rooms[0].DeleteReason, "container in use") {
		t.Fatalf("%+v %v", list, e)
	}
	r.hosts.failBoxes(nil)
	if err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET due_at=0")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.settle(t, ctx, room)
}

func TestHostLimitClearsARoomLimitedRefusal(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, worker := r.room(t, ctx, "room")
	other, _, _ := r.room(t, ctx, "other")
	if err := r.st.AddHost(ctx, box.Host{Name: "limited", State: box.HostReady, CheckedAt: time.Unix(1000, 0), Rooms: []contract.RoomID{room}}); err != nil {
		t.Fatal(err)
	}
	r.refused(t, ctx, room, nil, []contract.HostName{"limited"}, "host limited")
	session := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if _, e := r.s.HostSetLimits(ctx, session, contract.HostSetLimitsRequest{Host: "limited", Rooms: []contract.RoomID{other}}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("a session changed a host limit: %v", e)
	}
	if _, e := r.s.HostSetLimits(ctx, deleteOwner, contract.HostSetLimitsRequest{Host: "missing", Rooms: []contract.RoomID{other}}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("unknown host: %v", e)
	}
	if _, e := r.s.HostSetLimits(ctx, deleteOwner, contract.HostSetLimitsRequest{Host: "limited", Rooms: []contract.RoomID{other}}); e != nil {
		t.Fatal(e)
	}
	r.delete(t, ctx, room)
	// A room on its way out cannot come back into a limit, and the refused write leaves the old limit whole.
	if _, e := r.s.HostSetLimits(ctx, deleteOwner, contract.HostSetLimitsRequest{Host: "limited", Rooms: []contract.RoomID{room}}); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("limit to a deleting room: %v", e)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM host_rooms WHERE host='limited' AND room=?", other); n != 1 {
		t.Fatalf("limit lost its room: %d", n)
	}
	r.settle(t, ctx, room)
	if _, e := r.s.HostSetLimits(ctx, deleteOwner, contract.HostSetLimitsRequest{Host: "limited"}); e != nil {
		t.Fatal(e)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM host_rooms WHERE host='limited'"); n != 0 {
		t.Fatalf("no rooms means every room: %d rows", n)
	}
}

func TestDeletingRoomRefusesNewWork(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, root, worker := r.room(t, ctx, "room")
	other, _, _ := r.room(t, ctx, "other")
	session := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	if _, e := r.s.RoomPause(ctx, deleteOwner, contract.RoomPauseRequest{Room: room, Paused: true}); e != nil {
		t.Fatal(e)
	}
	if err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET want='stop' WHERE session=?", worker)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.hosts.err = errors.New("hold the room")
	before := r.upload(t, room, "before")
	if _, e := r.s.RoomDelete(ctx, session, contract.RoomDeleteRequest{Room: room, Discard: true}); e == nil || e.Code != contract.ErrForbidden {
		t.Fatalf("a session deleted the room: %v", e)
	}
	if _, e := r.s.RoomDelete(ctx, deleteOwner, contract.RoomDeleteRequest{Room: room}); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("delete without discard: %v", e)
	}
	if _, e := r.s.RoomDelete(ctx, deleteOwner, contract.RoomDeleteRequest{Room: "missing", Discard: true}); e == nil || e.Code != contract.ErrNotFound {
		t.Fatalf("unknown room: %v", e)
	}
	r.delete(t, ctx, room)
	conflict := func(name string, e *contract.Error) {
		t.Helper()
		if e == nil || e.Code != contract.ErrConflict {
			t.Errorf("%s: %v", name, e)
		}
	}
	_, e := r.s.RoomDelete(ctx, deleteOwner, contract.RoomDeleteRequest{Room: room, Discard: true})
	conflict("second delete", e)
	_, e = r.s.SessionStart(ctx, deleteOwner, contract.SessionStartRequest{Room: room, Name: "late", Profile: "test", Parent: root})
	conflict("session", e)
	_, e = r.s.Post(ctx, deleteOwner, contract.PostRequest{Room: room, Text: "late"})
	conflict("owner post", e)
	_, e = r.s.RoomRepoAdd(ctx, deleteOwner, contract.RoomRepoAddRequest{Room: room, Repo: "owner/late"})
	conflict("repository", e)
	_, e = r.s.Post(ctx, session, contract.PostRequest{Room: room, Text: "late"})
	conflict("session post", e)
	_, e = r.s.Post(ctx, deleteOwner, contract.PostRequest{Room: other, Text: "late", Attachments: []contract.Attachment{before}})
	if e != nil {
		t.Errorf("the owner can still take a file out while the room goes: %v", e)
	}
	_, e = r.s.Post(ctx, deleteOwner, contract.PostRequest{Room: other, Text: "late", Mentions: []string{"@room:worker"}})
	if e == nil || e.Code != contract.ErrInvalid {
		t.Errorf("mention into a deleting room: %v", e)
	}
	_, e = r.s.TranscriptAppend(ctx, session, contract.TranscriptAppendRequest{Path: "a.jsonl", Bytes: []byte("x")})
	conflict("transcript", e)
	instructions, e := r.s.InstructionsGet(ctx, deleteOwner, contract.InstructionsGetRequest{Room: room})
	if e != nil {
		t.Fatal(e)
	}
	_, e = r.s.InstructionsSet(ctx, deleteOwner, contract.InstructionsSetRequest{Room: room, Base: instructions.Hash, Text: "late"})
	conflict("instructions", e)
	upload := httptest.NewRequest("POST", "/v1/files?name=late.txt&room="+string(room), bytes.NewBufferString("late"))
	upload.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	r.s.Files(deleteOwner, "").ServeHTTP(rec, upload)
	if rec.Code != http.StatusConflict {
		t.Errorf("upload: %d", rec.Code)
	}
	// Resuming the room and new deliveries cannot wake a box: every box now wants discard.
	if _, e := r.s.RoomPause(ctx, deleteOwner, contract.RoomPauseRequest{Room: room, Paused: false}); e != nil {
		t.Fatal(e)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM boxes b JOIN sessions s ON s.id=b.session WHERE s.room=? AND b.want!='discard'", room); n != 0 {
		t.Errorf("%d boxes do not want discard", n)
	}
	if ok, err := r.st.CanDispatch(ctx, worker); err != nil || ok {
		t.Errorf("dispatch: %v %v", ok, err)
	}
	r.hosts.err = nil
	r.settle(t, ctx, room)
}

func TestRoomDeleteExpiresPendingOperations(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, worker := r.room(t, ctx, "room")
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('op_pending','pr.open',?,'session',?,'owner','pending','{}','{}',x'01',0,99999999999)`, room, worker)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.delete(t, ctx, room)
	if n := r.count(t, ctx, "SELECT count(*) FROM operations WHERE id='op_pending' AND expires_at=?", int64(1000000)); n != 1 {
		t.Fatal("a pending request can still be approved")
	}
	r.settle(t, ctx, room)
}

func TestRoomDeleteWaitsForRunningWork(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, worker := r.room(t, ctx, "room")
	err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('op_run','pr.open',?,'session',?,'owner','running','{}','{}',x'01',0,0)`, room, worker)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.delete(t, ctx, room)
	for range 8 {
		passLifecycle(t, r.s, ctx)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM rooms WHERE id=?", room); n != 1 {
		t.Fatal("rows went while an operation ran")
	}
	if err := r.st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE operations SET state='failed' WHERE id='op_run'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.settle(t, ctx, room)
}

func TestRoomDeleteLeavesArchiveRefsToTheExpirySweep(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, worker := r.room(t, ctx, "room")
	repos := &fakeRepos{err: errors.New("github is down")}
	r.s.SetRepositories(repos)
	ref := "refs/dyna/archive/" + string(worker)
	if err := r.st.SaveArchives(ctx, []store.Archive{{Session: worker, Repo: "owner/repo", Ref: ref, Commit: "abc", CreatedAt: time.Unix(1, 0), ExpiresAt: time.Unix(99999, 0)}}); err != nil {
		t.Fatal(err)
	}
	r.delete(t, ctx, room)
	r.settle(t, ctx, room) // An outage does not hold the room.
	if err := r.s.ExpireArchives(ctx); err == nil {
		t.Fatal("the sweep reached no remote")
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM archives WHERE ref=?", ref); n != 1 {
		t.Fatalf("a failed sweep lost the ref's row: %d", n)
	}
	repos.err = nil
	r.s.now = func() time.Time { return time.Unix(9000, 0) }
	if err := r.s.ExpireArchives(ctx); err != nil {
		t.Fatal(err)
	}
	if repos.deleted != ref {
		t.Fatalf("deleted %q", repos.deleted)
	}
	if n := r.count(t, ctx, "SELECT count(*) FROM archives"); n != 0 {
		t.Fatalf("archive rows: %d", n)
	}
}

func TestRoomDeletePublishesThatTheRoomIsGone(t *testing.T) {
	r, ctx := newDeleteRig(t)
	room, _, _ := r.room(t, ctx, "room")
	stream := ownerStream(ctx, t, r.s, "", 0)
	r.delete(t, ctx, room)
	r.settle(t, ctx, room)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-stream:
			if _, ok := event.Body.(contract.RoomDeleted); ok {
				if event.Room != room || event.Seq != 0 {
					t.Fatalf("%+v", event)
				}
				return
			}
		case <-deadline:
			t.Fatal("no room.deleted")
		}
	}
}
