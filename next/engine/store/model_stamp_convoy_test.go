package store

import (
	"context"
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stampConvoyStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	_, e = s.writer.Exec(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',0),('other','r','other','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','','agent','ready','','',0,0);
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t','s',1,'','',0,0,0,0),('u','other',1,'','',0,0,0,0);`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.writer.Exec(`UPDATE turns SET started_seq=0; CREATE TABLE review_stamp_writes(n INTEGER); INSERT INTO review_stamp_writes VALUES(0); CREATE TRIGGER review_count_stamp AFTER UPDATE OF model_sent_at ON turns BEGIN UPDATE review_stamp_writes SET n=n+1; END`); e != nil {
		t.Fatal(e)
	}
	return s, dir
}
func TestWarmModelRouteBypassesUnrelatedWriterWaits(t *testing.T) {
	for _, kind := range []string{"cold-stamp", "end-turn", "close-session"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := stampConvoyStore(t)
			ctx := context.Background()
			if e := s.MarkTurnModelSent(ctx, "s", "t", time.Now()); e != nil {
				t.Fatal(e)
			}
			socketDir, err := os.MkdirTemp("", "stamp-sock-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(socketDir)
			socket := filepath.Join(socketDir, "up.sock")
			ln, e := net.Listen("unix", socket)
			if e != nil {
				t.Fatal(e)
			}
			var upstream atomic.Int64
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Drain before replying: the proxy disables keep-alive, so an early reply can close the socket while it is still writing the body.
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				upstream.Add(1)
				w.WriteHeader(200)
			})}
			go server.Serve(ln)
			defer server.Close()
			h := model.Handler(socket, "synthetic-key", "s", []string{"test-model"}, []string{"synthetic-account"}, "", nil, false, func(ctx context.Context) error { return s.MarkTurnModelSent(ctx, "s", "t", time.Now()) })
			call := func() int {
				r := httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"test-model"}`))
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w.Code
			}
			if code := call(); code != 200 {
				t.Fatal(code)
			}
			entered, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() { writerDone <- s.Write(ctx, func(*sql.Tx) error { close(entered); <-release; return nil }) }()
			<-entered
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			blockerDone := make(chan error, 1)
			blockerCtx := &writerAdmissionContext{Context: ctx, admitted: make(chan struct{})}
			go func() {
				switch kind {
				case "cold-stamp":
					blockerDone <- s.MarkTurnModelSent(blockerCtx, "other", "u", time.Now())
				case "end-turn":
					blockerDone <- s.EndTurn(blockerCtx, "other", contract.TurnOutputRequest{Turn: "u", Outcome: contract.TurnCompleted}, contract.Event{ID: "end", Time: time.Now(), Room: "r", From: contract.Principal{Kind: contract.PrincipalSession, Session: "other"}, Body: contract.TurnEnded{Session: "other", Turn: "u", Outcome: contract.TurnCompleted}})
				case "close-session":
					blockerDone <- s.CloseSession(blockerCtx, "other", contract.Event{ID: "close", Time: time.Now(), Room: "r", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionClosed{Session: "other"}})
				}
			}()
			// The unbuffered single-writer admission owns s.mu while waiting to enqueue.
			// Wait for that boundary, not the recorder mutex whose convoy is under test.
			awaitStoreSignal(t, blockerCtx.admitted)

			canceled, cancel := context.WithCancel(ctx)
			cancel()
			cached := make(chan error, 1)
			go func() { cached <- s.MarkTurnModelSent(canceled, "s", "t", time.Now()) }()
			select {
			case err := <-cached:
				if err != nil {
					t.Errorf("warm canceled-context stamp: %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Error("warm canceled-context stamp convoyed behind unrelated writer wait")
			}
			done := make(chan int, 1)
			at := time.Now()
			code := 0
			go func() { done <- call() }()
			select {
			case code = <-done:
				close(release)
				if code != 200 {
					t.Errorf("status=%d", code)
				}
			case <-time.After(250 * time.Millisecond):
				close(release)
				t.Error("warm model request convoyed behind unrelated writer wait")
			}
			t.Logf("actual model.Handler: upstream calls while blocked=%d (baseline=1)", upstream.Load())
			if code == 0 {
				code = <-done
			}
			t.Logf("actual model.Handler warm request code=%d duration=%s upstream=%d", code, time.Since(at), upstream.Load())
			if code != 200 {
				t.Fatal(code)
			}
			if e := <-writerDone; e != nil {
				t.Fatal(e)
			}
			if e := <-blockerDone; e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestColdModelHandoffDoesNotWaitForStampWriter(t *testing.T) {
	s, _ := stampConvoyStore(t)
	ctx := context.Background()
	entered, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { writerDone <- s.Write(ctx, func(*sql.Tx) error { close(entered); <-release; return nil }) }()
	<-entered
	socketDir, err := os.MkdirTemp("", "stamp-cold-")
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer os.RemoveAll(socketDir)
	listener, err := net.Listen("unix", filepath.Join(socketDir, "up.sock"))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Consume the request before closing a non-keep-alive connection; an early response can race the proxy's body write.
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})}
	go server.Serve(listener)
	defer server.Close()
	recorded := make(chan error, 1)
	// Match runtime: the task uses engine lifetime, not the HTTP request context.
	route := model.Handler(listener.Addr().String(), "key", "s", []string{"allowed"}, []string{"account"}, "", nil, false, func(_ context.Context) error {
		go func() { recorded <- s.MarkTurnModelSent(ctx, "s", "t", time.UnixMilli(123)) }()
		return nil
	})
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		route.ServeHTTP(w, httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"allowed"}`)).WithContext(requestCtx))
		done <- w.Code
	}()
	completed := false
	select {
	case code := <-done:
		completed = true
		if code != http.StatusAccepted {
			t.Errorf("status=%d", code)
		}
	case <-time.After(250 * time.Millisecond):
		t.Error("cold model request waited for telemetry writer")
	}
	cancelRequest() // Request lifetime must not cancel the queued engine stamp.
	close(release)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if err := <-recorded; err != nil {
		t.Fatal(err)
	}
	var stamp int64
	if err := s.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id='t'").Scan(&stamp); err != nil || stamp != 123 {
		t.Fatalf("stamp=%d err=%v", stamp, err)
	}
	if !completed {
		if code := <-done; code != http.StatusAccepted {
			t.Errorf("status after writer release=%d", code)
		}
	}
}
