package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptReadOwnerAndTurn(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	var seq int64
	if err := s.store.Read().QueryRowContext(ctx, "SELECT seq FROM deliveries WHERE to_kind='session' AND to_id=? LIMIT 1", caller.Session).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", caller.Session, seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf("{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:00Z\",\"message\":{\"role\":\"user\",\"deliveryId\":\"%d\"}}\n{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:01Z\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"toolCall\",\"id\":\"c\",\"name\":\"read\",\"arguments\":{\"path\":\"a.go\"}}]}}\n", seq)
	if _, e := appendTranscript(s, ctx, caller, "a.jsonl", 0, data); e != nil {
		t.Fatal(e)
	}
	req := contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a"}
	got, e := s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req)
	if e != nil || len(got.Steps) != 0 {
		t.Fatalf("read = %+v %v", got, e)
	}
	req.Turn = "other"
	got, e = s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, req)
	if e != nil || len(got.Steps) != 0 {
		t.Fatalf("filter = %+v %v", got, e)
	}
}

func TestTranscriptTurnPageAndFullOutput(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	wake, problem := s.Post(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.PostRequest{Room: session.Room, Home: contract.Conversation{Kind: contract.ConversationDM, Session: caller.Session}, Text: "wake"})
	if problem != nil {
		t.Fatal(problem)
	}
	delivery := wake.Seq
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		var input contract.EventID
		if err := tx.QueryRowContext(ctx, "SELECT id FROM events WHERE seq=?", delivery).Scan(&input); err != nil {
			return err
		}
		event := contract.Event{ID: "transcript-start", Room: session.Room, From: caller, Time: time.Unix(1, 0), Body: contract.TurnStarted{Session: caller.Session, Turn: "turn_a", Inputs: []contract.EventID{input}}}
		seq, err := s.store.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_a',?,0,'','',0,0,0,0,?)", caller.Session, seq); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE deliveries SET turn='turn_a' WHERE to_kind='session' AND to_id=? AND seq=?", caller.Session, delivery)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	post, postErr := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "sent during turn", ToolCallID: "post-call"})
	if postErr != nil {
		t.Fatal(postErr)
	}
	event, err := s.store.Event(ctx, post.Seq)
	if err != nil || event.Body.(contract.PostCreated).Turn != "turn_a" {
		t.Fatalf("post=%+v err=%v", event, err)
	}
	for i := 0; i < maxTranscriptCallPosts; i++ {
		if _, problem := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "another", ToolCallID: "post-call"}); problem != nil {
			t.Fatal(problem)
		}
	}
	other, otherErr := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "another call", ToolCallID: "call1"})
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	unclaimed, unclaimedErr := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: "unclaimed"})
	if unclaimedErr != nil {
		t.Fatal(unclaimedErr)
	}
	output := strings.Repeat("x", 1200) + " token=secretvalue123456"
	data := fmt.Sprintf(`{"type":"message","id":"user","message":{"role":"user","deliveryId":"%d"}}
{"type":"message","id":"assistant","message":{"role":"assistant","content":[{"type":"toolCall","id":"call1","name":"bash","arguments":{}},{"type":"toolCall","id":"post-call","name":"bash","arguments":{"command":"dyna post hello"}}]}}
{"type":"message","id":"result","message":{"role":"toolResult","toolCallId":"call1","content":[{"type":"text","text":%q}]}}
{"type":"message","id":"post-result","message":{"role":"toolResult","toolCallId":"post-call","content":[{"type":"text","text":%q}]}}
`, delivery, output, post.Event)
	for i := 0; i < 30; i++ {
		data += fmt.Sprintf("{\"type\":\"message\",\"id\":\"tool-%d\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"toolCall\",\"id\":\"extra-%d\",\"name\":\"read\",\"arguments\":{}}]}}\n", i, i)
	}
	data += `{"type":"message","id":"final","message":{"role":"assistant","content":[{"type":"text","text":"final answer"}]}}` + "\n"
	ownerSub, ownerLive := s.live.Subscribe("", 1)
	roomSub, roomLive := s.live.Subscribe(session.Room, 1)
	defer s.live.Unsubscribe(ownerSub)
	defer s.live.Unsubscribe(roomSub)
	split := strings.Index(data, "\n") - 2
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", 0, data[:split]); problem != nil {
		t.Fatal(problem)
	}
	select {
	case event := <-ownerLive:
		t.Fatalf("incomplete record sent notice: %+v", event)
	default:
	}
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(split), data[split:]); problem != nil {
		t.Fatal(problem)
	}
	select {
	case event := <-ownerLive:
		changed, ok := event.Body.(contract.TranscriptChanged)
		if !ok || changed.Session != caller.Session || changed.Turn != "turn_a" {
			t.Fatalf("notice=%+v", event)
		}
	default:
		t.Fatal("owner did not receive transcript notice")
	}
	select {
	case event := <-roomLive:
		t.Fatalf("room received owner-only notice: %+v", event)
	default:
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE turns SET ended_at=1 WHERE id='turn_a'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(len(data)), " "); problem != nil {
		t.Fatal(problem)
	}
	select {
	case event := <-ownerLive:
		if event.Body.(contract.TranscriptChanged).Turn != "turn_a" {
			t.Fatal(event)
		}
	default:
		t.Fatal("late append did not notify owner")
	}
	ownerLive <- contract.Event{}
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(len(data)+1), " "); problem != nil {
		t.Fatal(problem)
	}
	<-ownerLive
	if _, ok := <-ownerLive; ok {
		t.Fatal("full owner stream did not close for reconnect")
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	started := time.Now()
	page, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	encoded, _ := json.Marshal(page)
	t.Logf("transcript.read: %s, %d bytes", time.Since(started), len(encoded))
	inputEvent, err := s.store.Event(ctx, delivery)
	if err != nil {
		t.Fatal(err)
	}
	if problem != nil || len(page.Turns) != 1 || len(page.Turns[0].Inputs) != 1 || !page.Turns[0].Inputs[0].At.Equal(inputEvent.Time) || page.Turns[0].Inputs[0].Home == nil || page.Turns[0].Inputs[0].Home.Kind != contract.ConversationDM || page.Turns[0].Inputs[0].Seq != delivery || len(page.Turns[0].Items[0].Posts) != 1 || page.Turns[0].Items[0].Posts[0].Event != other.Event || len(page.Turns[0].Items[1].Posts) != maxTranscriptCallPosts || page.Turns[0].Items[1].Posts[0].Event != post.Event || page.Turns[0].Items[1].PostsOmitted != 1 || !page.Turns[0].Items[0].More || len(page.Turns[0].Items[0].Output) != 1024 {
		t.Fatalf("page=%+v error=%v", page, problem)
	}
	finalFound, unclaimedFound, toolCount := false, false, 0
	for _, item := range page.Turns[0].Items {
		if item.Kind == contract.TranscriptPost && (item.From == nil || *item.From != caller) {
			t.Fatalf("standalone post author: %+v", item.From)
		}
		for _, post := range item.Posts {
			if post.From == nil || *post.From != caller {
				t.Fatalf("nested post author: %+v", post.From)
			}
		}
		if item.Text == "final answer" {
			finalFound = true
		}
		if item.Event == unclaimed.Event {
			unclaimedFound = true
		}
		if item.Kind == contract.TranscriptTool {
			toolCount++
		}
	}
	if !finalFound || !unclaimedFound || toolCount != 32 {
		t.Fatalf("final=%v unclaimed=%v tools=%d", finalFound, unclaimedFound, toolCount)
	}
	zero, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Page: contract.TranscriptPageOlderItems})
	if problem != nil || len(zero.Turns[0].Inputs) != 0 || len(zero.Turns[0].Items) == 0 {
		t.Fatalf("zero-index continuation=%+v error=%v", zero, problem)
	}
	full, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Item: "call1"})
	if problem != nil || strings.Contains(full.Chunk, "secretvalue") || !strings.Contains(full.Chunk, "[masked]") {
		t.Fatalf("full=%+v error=%v", full, problem)
	}
	var large strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&large, "{\"type\":\"message\",\"id\":\"large-%d\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"toolCall\",\"id\":\"large-call-%d\",\"name\":\"read\",\"arguments\":{\"path\":%q}}]}}\n", i, i, strings.Repeat("x", 8000))
	}
	large.WriteString(`{"type":"message","id":"last","message":{"role":"assistant","content":[{"type":"text","text":"last answer"}]}}` + "\n")
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(len(data)+2), large.String()); problem != nil {
		t.Fatal(problem)
	}
	budgeted, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	wire, _ := json.Marshal(budgeted.Turns[0])
	lastFound := false
	for _, item := range budgeted.Turns[0].Items {
		if item.Text == "last answer" {
			lastFound = true
		}
	}
	if problem != nil || len(wire) > 180<<10 || !budgeted.Turns[0].OlderItems || !lastFound {
		t.Fatalf("budgeted bytes=%d older=%v last=%v error=%v", len(wire), budgeted.Turns[0].OlderItems, lastFound, problem)
	}
	more, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Page: contract.TranscriptPageOlderItems, Cursor: budgeted.Turns[0].NextItem})
	if problem != nil || len(more.Turns[0].Items) == 0 || (more.Turns[0].OlderItems && more.Turns[0].NextItem <= budgeted.Turns[0].NextItem) {
		t.Fatalf("continuation did not advance: error=%v", problem)
	}

	for i := 0; i < 12; i++ {
		if _, problem := s.Post(ctx, caller, contract.PostRequest{Room: session.Room, Text: strings.Repeat("\x01", 4000)}); problem != nil {
			t.Fatal(problem)
		}
	}
	overflow, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	wire, _ = json.Marshal(overflow.Turns[0])
	finalFound = false
	for _, item := range overflow.Turns[0].Items {
		if item.Text == "last answer" {
			finalFound = true
		}
	}
	if problem != nil || len(wire) > 180<<10 || !overflow.Turns[0].MandatoryPosts || !finalFound {
		t.Fatalf("mandatory overflow bytes=%d posts=%v final=%v error=%v", len(wire), overflow.Turns[0].MandatoryPosts, finalFound, problem)
	}
	seenPosts := map[string]bool{}
	cursor := overflow.Turns[0]
	for pages := 0; ; pages++ {
		for _, item := range cursor.Items {
			if item.Text == strings.Repeat("\x01", 4000) {
				seenPosts[item.ID] = true
			}
		}
		if !cursor.MandatoryPosts {
			break
		}
		if pages >= 32 {
			t.Fatal("mandatory post continuation did not advance")
		}
		next, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Page: contract.TranscriptPageMandatoryPosts, Cursor: cursor.NextMandatoryPost})
		if problem != nil {
			t.Fatal(problem)
		}
		wire, _ := json.Marshal(next)
		if len(wire) > 256<<10 {
			t.Fatalf("response bytes=%d", len(wire))
		}
		cursor = next.Turns[0]
	}
	if len(seenPosts) != 12 {
		t.Fatalf("saw %d of 12 posts", len(seenPosts))
	}
	window := `{"type":"message","id":"pinned","message":{"role":"assistant","content":[{"type":"text","text":"pinned final"}]}}` + "\n"
	window += strings.Repeat(`{"type":"message","id":"extra","message":{"role":"assistant","content":[{"type":"toolCall","id":"repeat","name":"read","arguments":{}}]}}`+"\n", 300)
	if _, problem := appendTranscript(s, ctx, caller, "a.jsonl", int64(len(data)+2+large.Len()), window); problem != nil {
		t.Fatal(problem)
	}
	bounded, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session})
	pinned := false
	for _, item := range bounded.Turns[0].Items {
		if item.Text == "pinned final" {
			pinned = true
		}
	}
	if problem != nil || !bounded.Turns[0].MandatoryItems || !pinned {
		t.Fatalf("recent window pinned=%v error=%v", pinned, problem)
	}
	next, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_a", Page: contract.TranscriptPageMandatoryItems, Cursor: bounded.Turns[0].NextMandatory})
	if problem != nil || !next.Turns[0].MandatoryItems || next.Turns[0].NextMandatory <= bounded.Turns[0].NextMandatory {
		t.Fatalf("item cursor did not advance: %v", problem)
	}

}

func TestTranscriptInputAttachmentsAndBudget(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	attachments := make([]contract.Attachment, 16)
	for i := range attachments {
		attachments[i] = contract.Attachment{Name: "token=secretvalue123456 " + strings.Repeat("\x01", 200), Media: strings.Repeat("\x01", 200), SHA256: strings.Repeat("a", 64), Bytes: 42}
	}
	checks := make([]contract.VisualCheck, 16)
	for i := range checks {
		checks[i].Detail = strings.Repeat("\x01", 200)
	}
	attachments[0].Visual = &contract.VisualCard{Title: "token=secretvalue123456", Checks: checks}
	ids := make([]contract.EventID, 6)
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		for i := range ids {
			ids[i] = contract.EventID(fmt.Sprintf("input-%d", i))
			event := contract.Event{ID: ids[i], Room: session.Room, From: contract.Principal{Kind: contract.PrincipalOwner}, Time: time.Unix(1, 0), Body: contract.PostCreated{Text: "wake", Home: contract.Conversation{Kind: contract.ConversationDM, Session: caller.Session}, Attachments: attachments}}
			if _, err := s.store.AppendEvent(tx, event); err != nil {
				return err
			}
		}
		event := contract.Event{ID: "attachment-start", Room: session.Room, From: caller, Time: time.Unix(2, 0), Body: contract.TurnStarted{Session: caller.Session, Turn: "turn_attachments", Inputs: ids}}
		seq, err := s.store.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_attachments',?,0,'','',0,0,0,0,?)", caller.Session, seq)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	after, count := "", 0
	for pages := 0; ; pages++ {
		page, problem := s.TranscriptRead(ctx, owner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_attachments", AfterItem: after})
		if problem != nil {
			t.Fatal(problem)
		}
		wire, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Turns []struct {
				Inputs []struct {
					Attachments []contract.Attachment `json:"attachments"`
				} `json:"inputs"`
			} `json:"turns"`
		}
		if err := json.Unmarshal(wire, &decoded); err != nil {
			t.Fatal(err)
		}
		turnBytes, _ := json.Marshal(page.Turns[0])
		if len(turnBytes) > 180<<10 || len(wire) > 256<<10 {
			t.Fatalf("turn=%d response=%d", len(turnBytes), len(wire))
		}
		for _, input := range decoded.Turns[0].Inputs {
			if len(input.Attachments) != 16 {
				t.Fatalf("attachments=%d", len(input.Attachments))
			}
			a := input.Attachments[0]
			if strings.Contains(a.Name, "secretvalue") || len(a.Name) > 128 || len(a.Media) > 128 || a.SHA256 != strings.Repeat("a", 64) || a.Bytes != 42 || a.Visual == nil || strings.Contains(a.Visual.Title, "secretvalue") || len(a.Visual.Checks[0].Detail) > 128 {
				t.Fatalf("attachment was not safely preserved: %+v", a)
			}
			count++
		}
		if !page.Turns[0].More {
			break
		}
		if pages >= 6 || page.Turns[0].After == "" || page.Turns[0].After == after {
			t.Fatal("input cursor did not advance")
		}
		after = page.Turns[0].After
	}
	if count != len(ids) {
		t.Fatalf("received %d of %d inputs", count, len(ids))
	}
}

// An unavailable attribution table must not affect paths that never read Pi.
func TestTranscriptDefersUnusedMaps(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.Write(ctx, func(tx *sql.Tx) error {
		input := contract.Event{ID: "lazy-input", Room: session.Room, From: contract.Principal{Kind: contract.PrincipalOwner}, Time: time.Unix(1, 0), Body: contract.PostCreated{Text: "input chunk", Home: feedHome}}
		if _, err := s.store.AppendEvent(tx, input); err != nil {
			return err
		}
		start := contract.Event{ID: "lazy-start", Room: session.Room, From: caller, Time: time.Unix(2, 0), Body: contract.TurnStarted{Session: caller.Session, Turn: "lazy", Inputs: []contract.EventID{input.ID}}}
		seq, err := s.store.AppendEvent(tx, start)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('lazy',?,0,'','',0,0,0,0,?)`, caller.Session, seq); err != nil {
			return err
		}
		_, err = tx.Exec(`DROP TABLE deliveries`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, req := range []contract.TranscriptReadRequest{
		{Session: caller.Session, Turn: "lazy", Item: "input:lazy-input"},
		{Session: caller.Session, Turn: "lazy", AfterItem: "input:lazy-input"},
		{Session: caller.Session, Turn: "lazy"},
		{Session: caller.Session, Turn: "absent"},
	} {
		got, problem := s.TranscriptRead(ctx, theOwner, req)
		if problem != nil {
			t.Fatal(problem)
		}
		if req.Item != "" && got.Chunk != "input chunk" {
			t.Fatalf("chunk: %+v", got)
		}
	}
}

func TestTranscriptFeedMessagesAndSent(t *testing.T) {
	s, ctx, caller := transcriptCaller(t)
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		t.Fatal(err)
	}
	longReply := strings.Repeat("reply ", 900)
	var inputSeq, steerSeq int64
	var sentCallID, sentLooseID contract.EventID
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		var err error
		inputEv := contract.Event{ID: "feed-in", Room: session.Room, From: contract.Principal{Kind: contract.PrincipalEngine}, Time: time.Unix(1, 0), Body: contract.ServiceMessage{Service: "agent-chat", ID: "m1", Author: "@alice/team", Text: "check `worker-2`"}}
		if inputSeq, err = s.store.AppendEvent(tx, inputEv); err != nil {
			return err
		}
		start := contract.Event{ID: "feed-start", Room: session.Room, From: caller, Time: time.Unix(2, 0), Body: contract.TurnStarted{Session: caller.Session, Turn: "turn_feed", Inputs: []contract.EventID{inputEv.ID}}}
		startSeq, err := s.store.AppendEvent(tx, start)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,started_seq) VALUES('turn_feed',?,0,'','',0,0,0,0,?)`, caller.Session, startSeq); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state,turn) VALUES('session',?,?,'session','none','','','','',1,'done','turn_feed')`, caller.Session, inputSeq); err != nil {
			return err
		}
		steerEv := contract.Event{ID: "feed-steer", Room: session.Room, From: contract.Principal{Kind: contract.PrincipalEngine}, Time: time.Unix(4, 0), Body: contract.ServiceMessage{Service: "agent-chat", ID: "m2", Author: "@bob/team", Text: "also check logs"}}
		if steerSeq, err = s.store.AppendEvent(tx, steerEv); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deliveries(to_kind,to_id,seq,surface,reply_kind,reply_target_kind,reply_target_id,reply_home,reply_to,wake,state,turn,queue) VALUES('session',?,?,'session','none','','','','',1,'done','turn_feed','steer')`, caller.Session, steerSeq); err != nil {
			return err
		}
		sentCallID = "sent-call"
		if _, err := s.store.AppendEvent(tx, contract.Event{ID: sentCallID, Room: session.Room, From: caller, Time: time.Unix(5, 0), Body: contract.ServiceSent{Service: "agent-chat", ID: "out_1", To: "@alice/team", Text: longReply, ToolCallID: "svc-call"}}); err != nil {
			return err
		}
		sentLooseID = "sent-loose"
		_, err = s.store.AppendEvent(tx, contract.Event{ID: sentLooseID, Room: session.Room, From: caller, Time: time.Unix(6, 0), Body: contract.ServiceSent{Service: "agent-chat", ID: "out_2", To: "@bob/team", Text: "logs look fine"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lines := fmt.Sprintf(`{"type":"message","id":"u1","timestamp":"1970-01-01T00:00:02Z","message":{"role":"user","deliveryId":"%d"}}`+"\n"+
		`{"type":"message","id":"a1","timestamp":"1970-01-01T00:00:03Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"svc-call","name":"mcp__dyna__service","arguments":{"action":"send"}}]}}`+"\n"+
		`{"type":"message","id":"r1","timestamp":"1970-01-01T00:00:03Z","message":{"role":"toolResult","toolCallId":"svc-call","content":[{"type":"text","text":"{\"id\":\"out_1\"}"}]}}`+"\n"+
		`{"type":"message","id":"u2","timestamp":"1970-01-01T00:00:04Z","message":{"role":"user","deliveryId":"%d"}}`+"\n", inputSeq, steerSeq)
	if _, problem := appendTranscript(s, ctx, caller, "feed.jsonl", 0, lines); problem != nil {
		t.Fatal(problem)
	}
	page, problem := s.TranscriptRead(ctx, theOwner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_feed"})
	if problem != nil || len(page.Turns) != 1 {
		t.Fatalf("read failed: problem=%v turns=%d", problem, len(page.Turns))
	}
	turn := page.Turns[0]
	if len(turn.Inputs) != 1 || turn.Inputs[0].Type != (contract.ServiceMessage{}).EventType() {
		t.Fatalf("unexpected inputs: %+v", turn.Inputs)
	}
	var sawSteer, sawNestedSent, sawLooseSent bool
	for _, item := range turn.Items {
		if item.Kind == contract.TranscriptSteer && item.Type == (contract.ServiceMessage{}).EventType() && item.Event == "feed-steer" {
			sawSteer = true
		}
		for _, post := range item.Posts {
			if post.Event == sentCallID && post.Type == (contract.ServiceSent{}).EventType() && post.Service == "agent-chat" && post.To == "@alice/team" && post.More {
				sawNestedSent = true
			}
		}
		if item.Kind == contract.TranscriptPost && item.Event == sentLooseID && item.Type == (contract.ServiceSent{}).EventType() && item.Service == "agent-chat" && item.To == "@bob/team" && item.Text == "logs look fine" {
			sawLooseSent = true
		}
	}
	if !sawSteer || !sawNestedSent || !sawLooseSent {
		t.Fatalf("steer=%v nested=%v loose=%v items=%+v", sawSteer, sawNestedSent, sawLooseSent, turn.Items)
	}
	if len(page.Steps) != 1 || page.Steps[0].Tool != "mcp__dyna__service" {
		t.Fatalf("expected only tool step (no blank step for loose service.sent), got %+v", page.Steps)
	}
	chunk, problem := s.TranscriptRead(ctx, theOwner, contract.TranscriptReadRequest{Session: caller.Session, Turn: "turn_feed", Item: string(sentCallID), Part: contract.TranscriptPartText})
	if problem != nil || chunk.Chunk != longReply {
		t.Fatalf("chunk problem=%v len=%d want=%d", problem, len(chunk.Chunk), len(longReply))
	}
	digest, problem := s.TranscriptDigest(ctx, theOwner, contract.TranscriptDigestRequest{Session: string(caller.Session), Turn: "turn_feed"})
	if problem != nil || len(digest.Turns) != 1 {
		t.Fatalf("digest failed: problem=%v turns=%d", problem, len(digest.Turns))
	}
	joined := strings.Join(digest.Turns[0].Steps, "\n")
	if !strings.Contains(joined, "→ @alice/team: ") || !strings.Contains(joined, "→ @bob/team: logs look fine") {
		t.Fatalf("digest missing service.sent steps: %q", joined)
	}
}
