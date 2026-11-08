package engine

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/model"
)

// Real framed delivery and engine model route, real clock and local fake child proxy.
// These samples measure instrumentation locally, not production/provider latency.
func TestFramedModelHandoffTimestamps(t *testing.T) {
	s, room, worker, ctx := setup(t)
	var live atomic.Bool
	original := s.now
	s.now = func() time.Time {
		if live.Load() {
			return time.Now()
		}
		return original()
	}
	caller := boxCaller(worker)
	ch, p := testChannel(t, s, ctx, caller)
	if p != nil {
		t.Fatal(p)
	}
	first := (<-ch).Body.(contract.TurnStart)
	live.Store(true)
	socket := filepath.Join(t.TempDir(), "proxy.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: done\n\n"))
	})}
	go proxy.Serve(listener)
	defer proxy.Close()
	route := model.Handler(socket, "key", string(worker), []string{"allowed"}, []string{"account"}, "", false, func(ctx context.Context) error { return s.store.MarkTurnModelSent(ctx, worker, first.Turn, time.Now()) })
	request := func() {
		t.Helper()
		w := httptest.NewRecorder()
		route.ServeHTTP(w, httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"allowed","stream":true}`)))
		if w.Code != 200 {
			t.Fatalf("model status=%d", w.Code)
		}
	}
	post := func() {
		t.Helper()
		e, err := s.event(room, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostCreated{Text: "measure"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.store.Post(ctx, e, []contract.Delivery{{To: caller, Surface: contract.SurfaceEngine, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	ack := func(seq contract.DeliverySeq) {
		t.Helper()
		if _, p := s.DeliveryDone(ctx, caller, contract.DeliveryDoneRequest{Delivery: seq}); p != nil {
			t.Fatal(p)
		}
	}
	for _, input := range first.Inputs {
		ack(input.Delivery)
	}
	var postTaken, takenSent, startSent, steerPostTaken, steerRelation []int64
	for range 30 {
		if _, p := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: first.Turn, Outcome: contract.TurnCompleted}); p != nil {
			t.Fatal(p)
		}
		post()
		select {
		case msg := <-ch:
			first = msg.Body.(contract.TurnStart)
		case <-time.After(3 * time.Second):
			t.Fatal("no start")
		}
		for _, input := range first.Inputs {
			ack(input.Delivery)
		}
		var before int64
		if err := s.store.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id=?", first.Turn).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if before != 0 {
			t.Fatalf("setup stamped model: %d", before)
		}
		request()
		var posted, taken, sent, started int64
		if err := s.store.Read().QueryRow("SELECT e.at,d.taken_at,t.model_sent_at,t.started_at FROM deliveries d JOIN events e ON e.seq=d.seq JOIN turns t ON t.id=d.turn WHERE d.seq=? AND d.to_id=?", first.Inputs[0].Delivery, worker).Scan(&posted, &taken, &sent, &started); err != nil {
			t.Fatal(err)
		}
		if posted > taken || taken > sent || sent == 0 {
			t.Fatalf("post=%d taken=%d sent=%d", posted, taken, sent)
		}
		postTaken = append(postTaken, taken-posted)
		takenSent = append(takenSent, sent-taken)
		startSent = append(startSent, sent-started)
		// Ensure a steer arrives strictly after the first handoff at millisecond resolution.
		time.Sleep(2 * time.Millisecond)
		post()
		var steer contract.TurnSteer
		select {
		case msg := <-ch:
			steer = msg.Body.(contract.TurnSteer)
		case <-time.After(3 * time.Second):
			t.Fatal("no steer")
		}
		ack(steer.Input.Delivery)
		request() // A later request must not redefine the first handoff.
		var steerTaken, steerPosted, after int64
		if err := s.store.Read().QueryRow("SELECT e.at,d.taken_at,t.model_sent_at FROM deliveries d JOIN events e ON e.seq=d.seq JOIN turns t ON t.id=d.turn WHERE d.seq=? AND d.to_id=?", steer.Input.Delivery, worker).Scan(&steerPosted, &steerTaken, &after); err != nil {
			t.Fatal(err)
		}
		if after != sent || steerTaken <= sent {
			t.Fatalf("first overwritten or invalid steer: first=%d after=%d steer=%d", sent, after, steerTaken)
		}
		steerPostTaken = append(steerPostTaken, steerTaken-steerPosted)
		steerRelation = append(steerRelation, sent-steerTaken)
	}
	var originalSent, originalTaken int64
	if err := s.store.Read().QueryRow("SELECT t.model_sent_at,d.taken_at FROM turns t JOIN deliveries d ON d.turn=t.id WHERE t.id=? AND d.seq=? AND d.to_id=?", first.Turn, first.Inputs[0].Delivery, worker).Scan(&originalSent, &originalTaken); err != nil {
		t.Fatal(err)
	}
	if _, p := s.TurnActivity(ctx, caller, contract.TurnActivityRequest{Turn: first.Turn, Kind: contract.ActivityTool, Label: "heartbeat"}); p != nil {
		t.Fatal(p)
	}
	ch, p = testChannel(t, s, ctx, caller)
	if p != nil {
		t.Fatal(p)
	}
	select {
	case msg := <-ch:
		if msg.Body.(contract.TurnStart).Turn != first.Turn {
			t.Fatal("reconnect changed turn")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no replay")
	}
	request()
	var replaySent, replayTaken int64
	if err := s.store.Read().QueryRow("SELECT t.model_sent_at,d.taken_at FROM turns t JOIN deliveries d ON d.turn=t.id WHERE t.id=? AND d.seq=? AND d.to_id=?", first.Turn, first.Inputs[0].Delivery, worker).Scan(&replaySent, &replayTaken); err != nil {
		t.Fatal(err)
	}
	if replaySent != originalSent || replayTaken != originalTaken {
		t.Fatal("heartbeat or reconnect replaced timestamps")
	}
	for name, values := range map[string][]int64{"post_taken": postTaken, "taken_model_sent": takenSent, "start_model_sent": startSent, "steer_post_taken": steerPostTaken, "steer_first_sent_minus_taken": steerRelation} {
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		t.Logf("%s n=%d p50=%d ms p95=%d ms", name, len(values), values[len(values)/2], values[len(values)*95/100])
	}
}

func TestStoreStampFailureStillForwardsModel(t *testing.T) {
	s, _, worker, ctx := setup(t)
	ch, problem := testChannel(t, s, ctx, boxCaller(worker))
	if problem != nil {
		t.Fatal(problem)
	}
	first := (<-ch).Body.(contract.TurnStart)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER fail_model_stamp BEFORE UPDATE OF model_sent_at ON turns BEGIN SELECT RAISE(ABORT, 'test stamp failure'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "proxy.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	proxy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusAccepted) })}
	go proxy.Serve(listener)
	defer proxy.Close()
	route := model.Handler(socket, "key", string(worker), []string{"allowed"}, []string{"account"}, "", false, func(ctx context.Context) error { return s.store.MarkTurnModelSent(ctx, worker, first.Turn, time.Now()) })
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		route.ServeHTTP(w, httptest.NewRequest("POST", "/model/v1/messages", strings.NewReader(`{"model":"allowed"}`)))
		if w.Code != http.StatusAccepted {
			t.Fatalf("status=%d", w.Code)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("forwarded=%d", calls.Load())
	}
	var sent int64
	if err := s.store.Read().QueryRow("SELECT model_sent_at FROM turns WHERE id=?", first.Turn).Scan(&sent); err != nil || sent != 0 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
}
