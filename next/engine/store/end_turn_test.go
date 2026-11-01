package store

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

var (
	endTurnParent = contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	endTurnChild  = contract.Principal{Kind: contract.PrincipalSession, Session: "child"}
	endTurnOwner  = contract.Principal{Kind: contract.PrincipalOwner}
	endTurnFeed   = contract.Conversation{Kind: contract.ConversationFeed}
	endTurnDM     = contract.Conversation{Kind: contract.ConversationDM, Session: "child"}
)

// endTurnFixture runs child's turn up to its last post, with one input for each channel.
func endTurnFixture(t *testing.T, channels []contract.ReplyChannel, sent contract.PostCreated) (*Store, contract.Event) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1000, 0)
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.CreateRoom(ctx, "room", "room", now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.writer.ExecContext(ctx, "INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('parent','room','parent','p','open','',0),('child','room','child','p','open','',0); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) VALUES('a','parent','','agent','ready','','',0,0),('b','child','','agent','ready','','',0,0)"); err != nil {
		t.Fatal(err)
	}
	for i, channel := range channels {
		input := contract.Event{ID: contract.EventID(fmt.Sprintf("input%d", i)), Time: now, Room: "room", From: endTurnParent, Body: contract.PostCreated{Text: "work", Home: endTurnFeed}}
		if _, err := s.Post(ctx, input, []contract.Delivery{{To: endTurnChild, Surface: contract.SurfaceFeed, Reply: channel, Wake: true}}); err != nil {
			t.Fatal(err)
		}
	}
	start := contract.Event{ID: "start", Time: now, Room: "room", From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.TurnStarted{}}
	if _, _, _, err := s.Dispatch(ctx, "child", "turn", start, false); err != nil {
		t.Fatal(err)
	}
	var deliveries []contract.Delivery
	for _, mention := range sent.Mentions {
		deliveries = append(deliveries, contract.Delivery{To: mention, Surface: contract.SurfaceFeed, Reply: contract.ReplyChannel{Kind: contract.ReplyPost, Target: endTurnChild}, Wake: true})
	}
	post := contract.Event{ID: "reply", Time: now, Room: "room", From: endTurnChild, Body: sent}
	if post.Seq, err = s.Post(ctx, post, deliveries); err != nil {
		t.Fatal(err)
	}
	return s, post
}

func endChildTurn(t *testing.T, s *Store, output contract.TurnOutputRequest) (contract.TurnEnded, int64) {
	t.Helper()
	ctx := context.Background()
	output.Turn = "turn"
	end := contract.Event{ID: "end", Time: time.Unix(1000, 0), Room: "room", From: endTurnChild, Body: contract.TurnEnded{Session: "child", Turn: "turn", Outcome: output.Outcome, Final: output.Final, Cause: output.Cause}}
	if err := s.EndTurn(ctx, "child", output, end); err != nil {
		t.Fatal(err)
	}
	events, err := s.Events(ctx, "room", 0, 100, []contract.EventType{"turn.ended"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events %+v %v", events, err)
	}
	return events[0].Body.(contract.TurnEnded), events[0].Seq
}

// parentDeliveries lists the parent's deliveries as "seq wake".
func parentDeliveries(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.Read().Query("SELECT seq,wake FROM deliveries WHERE to_id='parent' ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var seq int64
		var wake bool
		if err := rows.Scan(&seq, &wake); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d %v", seq, wake))
	}
	return got
}

// A parent the end_turn post never mentioned still gets its answer: the post itself, which wakes it.
func TestEndPostAnswersAWaitingParent(t *testing.T) {
	channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed}
	s, post := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: endTurnDM})
	body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted, EndPosts: []contract.EventID{post.ID}})
	if body.Final != "" || len(body.Replies) != 1 || body.Replies[0].Posted != post.ID {
		t.Fatalf("turn %+v", body)
	}
	if got, want := parentDeliveries(t, s), []string{fmt.Sprintf("%d true", post.Seq)}; !slices.Equal(got, want) {
		t.Fatalf("deliveries %v, want %v", got, want)
	}
	var reply string
	if err := s.Read().QueryRow("SELECT reply_kind FROM deliveries WHERE to_id='parent'").Scan(&reply); err != nil || reply != string(contract.ReplyPost) {
		t.Fatalf("reply %q: %v", reply, err)
	}
}

func TestEndPostThatMentionedTheParentArrivesOnce(t *testing.T) {
	channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed}
	s, post := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: endTurnFeed, Mentions: []contract.Principal{endTurnParent}})
	body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted, EndPosts: []contract.EventID{post.ID}})
	if len(body.Replies) != 1 || body.Replies[0].Posted != post.ID {
		t.Fatalf("turn %+v", body)
	}
	if got, want := parentDeliveries(t, s), []string{fmt.Sprintf("%d true", post.Seq)}; !slices.Equal(got, want) {
		t.Fatalf("deliveries %v, want %v", got, want)
	}
}

func TestEndPostLinksEveryOpenFinalChannel(t *testing.T) {
	channels := []contract.ReplyChannel{{Kind: contract.ReplyFinal, Target: endTurnOwner, Home: endTurnFeed}, {Kind: contract.ReplyFinal, Target: endTurnOwner, Home: endTurnDM}, {Kind: contract.ReplyPost, Target: endTurnParent, Home: endTurnFeed}}
	s, post := endTurnFixture(t, channels, contract.PostCreated{Text: "report", Home: endTurnDM})
	body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted, EndPosts: []contract.EventID{post.ID}})
	if len(body.Replies) != 2 || body.Replies[0].Posted != post.ID || body.Replies[1].Posted != post.ID {
		t.Fatalf("replies %+v", body.Replies)
	}
}

func TestEndPostOnPostChannelOwesNothing(t *testing.T) {
	channel := contract.ReplyChannel{Kind: contract.ReplyPost, Target: endTurnParent, Home: endTurnFeed}
	s, post := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: endTurnFeed})
	body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted, EndPosts: []contract.EventID{post.ID}})
	if len(body.Replies) != 0 {
		t.Fatalf("replies %+v", body.Replies)
	}
	if got := parentDeliveries(t, s); len(got) != 0 {
		t.Fatalf("deliveries %v", got)
	}
}

// A failed post, a sibling tool or steering keeps the turn going, so the box reports no end post and
// the final text is the reply.
func TestTurnThatContinuedPastAPostEndsOnItsText(t *testing.T) {
	channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed}
	s, _ := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: endTurnDM})
	body, seq := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted, Final: "the answer"})
	if body.Final != "the answer" || len(body.Replies) != 1 || body.Replies[0].Posted != "" {
		t.Fatalf("turn %+v", body)
	}
	if got, want := parentDeliveries(t, s), []string{fmt.Sprintf("%d true", seq)}; !slices.Equal(got, want) {
		t.Fatalf("deliveries %v, want %v", got, want)
	}
}

func TestEndPostsMustBeTheSessionsOwn(t *testing.T) {
	channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed}
	s, _ := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: endTurnDM})
	body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted, Final: "the answer", EndPosts: []contract.EventID{"input0", "missing"}})
	if len(body.Replies) != 1 || body.Replies[0].Posted != "" {
		t.Fatalf("turn %+v", body)
	}
}

func TestFailedTurnIgnoresEndPosts(t *testing.T) {
	channel := contract.ReplyChannel{Kind: contract.ReplyFinal, Target: endTurnParent, Home: endTurnFeed}
	s, post := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: endTurnDM})
	body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnFailed, Cause: contract.CauseModelFailed, EndPosts: []contract.EventID{post.ID}})
	if len(body.Replies) != 1 || body.Replies[0].Posted != "" {
		t.Fatalf("turn %+v", body)
	}
}
