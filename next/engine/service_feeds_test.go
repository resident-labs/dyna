package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/api"
	"github.com/resident-labs/dyna/room"
)

func unixHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}

func setupFeedRig(t *testing.T) (*Service, *api.Sockets, contract.RoomID, contract.SessionID, contract.SessionID, *time.Time, context.Context) {
	t.Helper()
	s, roomID, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	var mu sync.Mutex
	clock := setupNow()
	s.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	list, err := s.store.SessionList(ctx, contract.SessionListRequest{Room: roomID}, 25)
	if err != nil {
		t.Fatal(err)
	}
	var rootID contract.SessionID
	for _, sess := range list.Sessions {
		if sess.Parent == "" {
			rootID = sess.ID
		}
	}
	if rootID == "" {
		t.Fatal("missing root session")
	}
	runtimeDir, err := os.MkdirTemp("", "dyna-feed-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeDir) })
	sockets := api.NewSockets(api.New(boxService{Service: s}, nil), runtimeDir, uint32(os.Getuid()), nil)
	t.Cleanup(func() { _ = sockets.Close() })
	return s, sockets, roomID, rootID, worker, &clock, ctx
}

func declareFeedService(t *testing.T, s *Service, ctx context.Context, roomID contract.RoomID, name contract.ServiceName, senders ...string) contract.ServiceSetResponse {
	t.Helper()
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	resp, problem := s.ServiceSet(ctx, owner, contract.ServiceSetRequest{
		Room: roomID,
		Spec: contract.ServiceSpec{Name: name, Image: svcDigest, Feed: true, Senders: senders},
	})
	if problem != nil {
		t.Fatalf("declare %s: %v", name, problem)
	}
	return resp
}

func TestServiceFeedSocketIdentityAndSpoofPrevention(t *testing.T) {
	s, sockets, roomA, _, _, _, ctx := setupFeedRig(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	// Create a second room to verify cross-room isolation.
	roomBResp, problem := s.RoomCreate(ctx, owner, contract.RoomCreateRequest{Name: "other-room", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	roomB := roomBResp.Room

	svcA := declareFeedService(t, s, ctx, roomA, "agent-chat").Service
	svcB := declareFeedService(t, s, ctx, roomB, "victim").Service
	if err := sockets.AddService(svcA.ID, svcA.Room, svcA.Spec.Name); err != nil {
		t.Fatal(err)
	}
	if err := sockets.AddService(svcB.ID, svcB.Room, svcB.Spec.Name); err != nil {
		t.Fatal(err)
	}
	clientA := unixHTTPClient(sockets.ServiceSocketPath(svcA.ID))

	// Attempt to spoof another service name and room in the JSON body: the engine ignores them and stamps svcA's identity.
	payload := `{"id":"msg-1","author":"@alice/chat","text":"hello","service":"victim","room":"` + string(roomB) + `"}`
	resp, err := clientA.Post("http://dyna/v1/messages", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Room B must receive zero service.message events.
	if evs := eventsOf[contract.ServiceMessage](t, s, ctx, roomB); len(evs) != 0 {
		t.Fatalf("room B received spoofed event: %+v", evs)
	}
	// Room A must receive the event stamped with agent-chat.
	evs := eventsOf[contract.ServiceMessage](t, s, ctx, roomA)
	if len(evs) != 1 || evs[0].Service != "agent-chat" || evs[0].Author != "@alice/chat" || evs[0].Text != "hello" {
		t.Fatalf("room A events = %+v", evs)
	}

	// Outbox isolation: queue a message for svcB, then try to read or ack it from svcA's socket.
	sent, problem := s.ServiceSend(ctx, owner, contract.ServiceSendRequest{Room: roomB, Service: "victim", To: "#general", Text: "secret"})
	if problem != nil {
		t.Fatal(problem)
	}
	outResp, err := clientA.Get("http://dyna/v1/outbox?wait=0s")
	if err != nil {
		t.Fatal(err)
	}
	defer outResp.Body.Close()
	var outbox contract.FeedOutboxResponse
	if err := json.NewDecoder(outResp.Body).Decode(&outbox); err != nil {
		t.Fatal(err)
	}
	if len(outbox.Messages) != 0 {
		t.Fatalf("svcA saw svcB outbox: %+v", outbox.Messages)
	}
	ackResp, err := clientA.Post("http://dyna/v1/outbox/"+sent.ID, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ackResp.Body.Close()
	if ackResp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-service ack status = %d, want 404", ackResp.StatusCode)
	}
}

func TestServiceFeedSizeRateLimitsAndDedupe(t *testing.T) {
	s, sockets, roomID, _, _, clock, ctx := setupFeedRig(t)
	svc := declareFeedService(t, s, ctx, roomID, "agent-chat").Service
	if err := sockets.AddService(svc.ID, svc.Room, svc.Spec.Name); err != nil {
		t.Fatal(err)
	}
	client := unixHTTPClient(sockets.ServiceSocketPath(svc.ID))

	post := func(body any) *http.Response {
		t.Helper()
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Post("http://dyna/v1/messages", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// 16 KiB exact limit succeeds; 16 KiB + 1 byte is rejected with 413.
	exact := post(contract.FeedMessageRequest{ID: "exact-16k", Author: "u", Text: strings.Repeat("a", contract.ServiceFeedTextMax)})
	exact.Body.Close()
	if exact.StatusCode != http.StatusOK {
		t.Fatalf("exact 16 KiB status = %d, want 200", exact.StatusCode)
	}
	tooBig := post(contract.FeedMessageRequest{ID: "over-16k", Author: "u", Text: strings.Repeat("a", contract.ServiceFeedTextMax+1)})
	tooBig.Body.Close()
	if tooBig.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("16 KiB + 1 status = %d, want 413", tooBig.StatusCode)
	}

	// Retry with the same id dedupes: 200 with duplicate=true, no second event, feed_in unchanged.
	dupResp := post(contract.FeedMessageRequest{ID: "exact-16k", Author: "u", Text: "retried"})
	var dupBody contract.FeedMessageResponse
	if err := json.NewDecoder(dupResp.Body).Decode(&dupBody); err != nil {
		t.Fatal(err)
	}
	dupResp.Body.Close()
	if dupResp.StatusCode != http.StatusOK || !dupBody.OK || !dupBody.Duplicate {
		t.Fatalf("dedupe response = %d %+v", dupResp.StatusCode, dupBody)
	}
	if evs := eventsOf[contract.ServiceMessage](t, s, ctx, roomID); len(evs) != 1 {
		t.Fatalf("events after retry = %d, want 1", len(evs))
	}
	shown, problem := s.ServiceShow(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.ServiceRef{Room: roomID, Name: "agent-chat"})
	if problem != nil || shown.Service.Feed == nil || shown.Service.Feed.MessagesIn != 1 || shown.Service.Feed.LastMessageAt == nil {
		t.Fatalf("feed stats after dedupe = %+v %v", shown.Service.Feed, problem)
	}

	// Rate limit: 3 calls already made in this minute; 57 more succeed (total 60), 61st returns 429.
	for i := range 57 {
		r := post(contract.FeedMessageRequest{ID: fmt.Sprintf("rl-%d", i), Author: "u", Text: "ok"})
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Fatalf("call %d status = %d, want 200", i+4, r.StatusCode)
		}
	}
	limited := post(contract.FeedMessageRequest{ID: "rl-over", Author: "u", Text: "blocked"})
	limited.Body.Close()
	if limited.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("61st call status = %d, want 429", limited.StatusCode)
	}

	// After 1 minute passes, calls succeed again.
	*clock = clock.Add(61 * time.Second)
	recovered := post(contract.FeedMessageRequest{ID: "rl-after", Author: "u", Text: "ok"})
	recovered.Body.Close()
	if recovered.StatusCode != http.StatusOK {
		t.Fatalf("after window status = %d, want 200", recovered.StatusCode)
	}
}

func TestServiceFeedRoutingWhereMatchAndDeliveryWording(t *testing.T) {
	s, sockets, roomID, rootID, workerID, _, ctx := setupFeedRig(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}

	// Declaring a feed automatically adds a service.failed subscription so a dead feed reaches root.
	setResp := declareFeedService(t, s, ctx, roomID, "agent-chat")
	if !strings.Contains(setResp.Note, "service.failed") {
		t.Fatalf("expected service.failed subscription note, got %q", setResp.Note)
	}
	// Updating the same feed does not duplicate the service.failed subscription.
	setAgain := declareFeedService(t, s, ctx, roomID, "agent-chat")
	if setAgain.Note != "" {
		t.Fatalf("unexpected duplicate note on update: %q", setAgain.Note)
	}
	svc := setResp.Service
	other := declareFeedService(t, s, ctx, roomID, "other-feed").Service
	if err := sockets.AddService(svc.ID, svc.Room, svc.Spec.Name); err != nil {
		t.Fatal(err)
	}
	if err := sockets.AddService(other.ID, other.Room, other.Spec.Name); err != nil {
		t.Fatal(err)
	}

	// Subscribe worker to service.message where service=agent-chat only.
	if _, problem := s.SubscriptionAdd(ctx, owner, contract.SubscriptionAddRequest{
		Scope: contract.ScopeRoom,
		Room:  roomID,
		On:    []contract.EventType{"service.message"},
		Where: []string{"service=agent-chat"},
		Wake:  string(workerID),
	}); problem != nil {
		t.Fatalf("add service.message subscription: %v", problem)
	}

	// Post on other-feed first: wakes nobody (neither worker nor root).
	workerBefore := pendingWakes(t, s, ctx, workerID)
	rootBefore := pendingWakes(t, s, ctx, rootID)
	otherClient := unixHTTPClient(sockets.ServiceSocketPath(other.ID))
	r0, err := otherClient.Post("http://dyna/v1/messages", "application/json", strings.NewReader(`{"id":"o1","author":"@x/y","text":"ignored"}`))
	if err != nil {
		t.Fatal(err)
	}
	r0.Body.Close()
	pass(t, s, ctx)
	if n := pendingWakes(t, s, ctx, workerID); n != workerBefore {
		t.Fatalf("worker woke on unmatched feed: pending=%d, want %d", n, workerBefore)
	}
	if n := pendingWakes(t, s, ctx, rootID); n != rootBefore {
		t.Fatalf("root woke on unmatched feed: pending=%d, want %d", n, rootBefore)
	}

	// Post on agent-chat: wakes worker with "[Dyna] ... From service agent-chat" and "<outside source=\"agent-chat\" author=\"@x/y\">", never "From the owner".
	chatClient := unixHTTPClient(sockets.ServiceSocketPath(svc.ID))
	r1, err := chatClient.Post("http://dyna/v1/messages", "application/json", strings.NewReader(`{"id":"m1","author":"@x/y","text":"ping from outside","data":{"room":"lobby"}}`))
	if err != nil {
		t.Fatal(err)
	}
	r1.Body.Close()
	pass(t, s, ctx)
	if n := pendingWakes(t, s, ctx, workerID); n != workerBefore+1 {
		t.Fatalf("worker pending wakes = %d, want %d", n, workerBefore+1)
	}
	if n := pendingWakes(t, s, ctx, rootID); n != rootBefore {
		t.Fatalf("root woke unexpectedly: pending=%d, want %d", n, rootBefore)
	}
	workerSess, err := s.store.Session(ctx, workerID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.store.Box(ctx, workerSess.Box)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.transition(ctx, roomID, b, contract.BoxReady, "", ""); err != nil {
		t.Fatal(err)
	}
	startEv, err := s.event(roomID, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{Session: workerID})
	if err != nil {
		t.Fatal(err)
	}
	_, inputs, _, err := s.store.Dispatch(ctx, workerID, "turn-feed-test", startEv, false)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, roomID, 0, 100, []contract.EventType{"service.message"})
	if err != nil || len(events) < 2 {
		t.Fatalf("events: %+v %v", events, err)
	}
	wantEvent := events[len(events)-1]
	found := false
	for _, in := range inputs {
		if int64(in.Seq) != wantEvent.Seq {
			continue
		}
		found = true
		frame, _, err := s.inputFrame(ctx, workerID, wantEvent, contract.Delivery{Surface: in.Surface, Reply: in.Reply})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(frame, "From service agent-chat.") {
			t.Fatalf("frame missing service attribution: %q", frame)
		}
		if strings.Contains(strings.ToLower(frame), "from the owner") {
			t.Fatalf("frame must never say From the owner: %q", frame)
		}
		rendered, err := room.RenderDelivery(wantEvent.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(rendered, `<outside source="agent-chat" author="@x/y">`) || !strings.Contains(rendered, "ping from outside") {
			t.Fatalf("rendered body missing outside wrapper or text:\n%s", rendered)
		}
	}
	if !found {
		t.Fatalf("worker did not receive service.message input (inputs=%+v)", inputs)
	}

	// Verify service.failed is deliverable and renders cleanly.
	if !room.Deliverable("service.failed") || !room.Deliverable("service.message") {
		t.Fatal("service.failed and service.message must be deliverable")
	}
	failRendered, err := room.RenderDelivery(contract.ServiceFailure{Service: svc.ID, Name: "agent-chat", Host: "h1", Reason: contract.ServiceCrashLoop})
	if err != nil || !strings.Contains(failRendered, "Service agent-chat failed on h1: crash_loop.") {
		t.Fatalf("service.failed render = %q, err = %v", failRendered, err)
	}
}

func TestServiceOutboxRetrySenderListAndAuditEvent(t *testing.T) {
	s, sockets, roomID, rootID, workerID, _, ctx := setupFeedRig(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	rootCaller := contract.Principal{Kind: contract.PrincipalSession, Session: rootID}
	workerCaller := contract.Principal{Kind: contract.PrincipalSession, Session: workerID}

	// Default senders: root only. Non-root worker is forbidden; root and owner succeed.
	svc := declareFeedService(t, s, ctx, roomID, "agent-chat").Service
	if err := sockets.AddService(svc.ID, svc.Room, svc.Spec.Name); err != nil {
		t.Fatal(err)
	}
	if _, problem := s.ServiceSend(ctx, workerCaller, contract.ServiceSendRequest{Service: "agent-chat", To: "#general", Text: "nope"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("non-root default send: %v, want forbidden", problem)
	}
	sent1, problem := s.ServiceSend(ctx, rootCaller, contract.ServiceSendRequest{Service: "agent-chat", To: "#general", Text: "from root"})
	if problem != nil || sent1.ID == "" {
		t.Fatalf("root send: %+v %v", sent1, problem)
	}

	// Non-feed service refuses service.send.
	if _, problem := s.ServiceSet(ctx, owner, contract.ServiceSetRequest{
		Room: roomID,
		Spec: contract.ServiceSpec{Name: "plain", Image: svcDigest},
	}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.ServiceSend(ctx, rootCaller, contract.ServiceSendRequest{Service: "plain", To: "#general", Text: "nope"}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("non-feed send: %v, want invalid", problem)
	}

	// Allowlist senders: only listed session may send (root is excluded unless listed).
	workerSess, err := s.store.Session(ctx, workerID)
	if err != nil {
		t.Fatal(err)
	}
	declareFeedService(t, s, ctx, roomID, "agent-chat", workerSess.Name)
	if _, problem := s.ServiceSend(ctx, rootCaller, contract.ServiceSendRequest{Service: "agent-chat", To: "#general", Text: "blocked"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("unlisted root send: %v, want forbidden", problem)
	}
	sent2, problem := s.ServiceSend(ctx, workerCaller, contract.ServiceSendRequest{Service: "agent-chat", To: "@bob", Text: "from worker"})
	if problem != nil || sent2.ID == "" {
		t.Fatalf("listed worker send: %+v %v", sent2, problem)
	}

	// Audit trail: two service.sent events in room log, feed.messagesOut == 2.
	audit := eventsOf[contract.ServiceSent](t, s, ctx, roomID)
	if len(audit) != 2 || audit[0].ID != sent1.ID || audit[1].ID != sent2.ID || audit[1].To != "@bob" || audit[1].Text != "from worker" {
		t.Fatalf("service.sent events = %+v", audit)
	}
	shown, problem := s.ServiceShow(ctx, owner, contract.ServiceRef{Room: roomID, Name: "agent-chat"})
	if problem != nil || shown.Service.Feed == nil || shown.Service.Feed.MessagesOut != 2 {
		t.Fatalf("feed stats = %+v %v", shown.Service.Feed, problem)
	}

	// Outbox polling & retry until acknowledged.
	client := unixHTTPClient(sockets.ServiceSocketPath(svc.ID))
	poll := func(wait string) []contract.FeedOutboxMessage {
		t.Helper()
		resp, err := client.Get("http://dyna/v1/outbox?wait=" + wait)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("outbox status = %d", resp.StatusCode)
		}
		var out contract.FeedOutboxResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out.Messages
	}

	first := poll("0s")
	if len(first) != 2 || first[0].ID != sent1.ID || first[1].ID != sent2.ID {
		t.Fatalf("first poll = %+v", first)
	}
	// Without acking, both are handed out again.
	retry := poll("0s")
	if len(retry) != 2 || retry[0].ID != sent1.ID {
		t.Fatalf("retry poll without ack = %+v", retry)
	}
	// Ack first; second stays pending until acked.
	ack1, err := client.Post("http://dyna/v1/outbox/"+sent1.ID, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	ack1.Body.Close()
	if ack1.StatusCode != http.StatusOK {
		t.Fatalf("ack1 status = %d", ack1.StatusCode)
	}
	afterAck1 := poll("0s")
	if len(afterAck1) != 1 || afterAck1[0].ID != sent2.ID {
		t.Fatalf("after ack1 = %+v", afterAck1)
	}
	// Ack second (twice, verifying idempotence).
	for range 2 {
		ack2, err := client.Post("http://dyna/v1/outbox/"+sent2.ID, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		ack2.Body.Close()
		if ack2.StatusCode != http.StatusOK {
			t.Fatalf("ack2 status = %d", ack2.StatusCode)
		}
	}
	if empty := poll("0s"); len(empty) != 0 {
		t.Fatalf("expected empty outbox, got %+v", empty)
	}

	// Long-poll wakes immediately when a new message is queued.
	gotCh := make(chan []contract.FeedOutboxMessage, 1)
	go func() { gotCh <- poll("5s") }()
	time.Sleep(30 * time.Millisecond)
	sent3, problem := s.ServiceSend(ctx, workerCaller, contract.ServiceSendRequest{Service: "agent-chat", To: "#live", Text: "wake poller"})
	if problem != nil {
		t.Fatal(problem)
	}
	select {
	case msgs := <-gotCh:
		if len(msgs) != 1 || msgs[0].ID != sent3.ID || msgs[0].Text != "wake poller" {
			t.Fatalf("long-poll got %+v", msgs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll did not wake on ServiceSend")
	}
}
