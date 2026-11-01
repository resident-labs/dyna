package room

import (
	"reflect"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestDeliveries(t *testing.T) {
	tree := NewTree([]Node{{ID: "root"}, {ID: "parent", Parent: "root"}, {ID: "child", Parent: "parent"}, {ID: "sibling", Parent: "parent"}})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	session := func(id contract.SessionID) contract.Principal {
		return contract.Principal{Kind: contract.PrincipalSession, Session: id}
	}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: "child"}
	for _, tt := range []struct {
		name    string
		from    contract.Principal
		surface contract.Surface
		to      contract.SessionID
		home    contract.Conversation
		replyTo contract.EventID
		group   bool
		want    contract.ReplyChannel
	}{
		{"owner feed", owner, contract.SurfaceFeed, "child", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed, ReplyTo: "event"}},
		{"owner unmentioned", owner, contract.SurfaceFeed, "", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed, ReplyTo: "event"}},
		{"owner dm", owner, contract.SurfaceDM, "child", dm, "", false, contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: dm}},
		{"brief", session("parent"), contract.SurfaceSession, "child", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyFinal, Target: session("parent"), Home: feed}},
		{"parent post", session("parent"), contract.SurfaceFeed, "child", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyFinal, Target: session("parent"), Home: feed}},
		{"card", owner, contract.SurfaceCard, "child", feed, "card", false, contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed, ReplyTo: "card"}},
		{"child result", session("child"), contract.SurfaceSession, "parent", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyPost, Target: session("child"), Home: feed}},
		{"sibling post", session("sibling"), contract.SurfaceFeed, "child", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyPost, Target: session("sibling"), Home: feed}},
		{"other room post", session("outside"), contract.SurfaceFeed, "child", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyPost, Target: session("outside"), Home: feed}},
		{"group", session("parent"), contract.SurfaceFeed, "child", feed, "", true, contract.ReplyChannel{Kind: contract.ReplyNone}},
		{"engine", contract.Principal{Kind: contract.PrincipalEngine}, contract.SurfaceEngine, "child", feed, "", false, contract.ReplyChannel{Kind: contract.ReplyNone}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := DeliveryInput{Event: contract.Event{ID: "event", Seq: 1, From: tt.from}, Surface: tt.surface, Home: tt.home, ReplyTo: tt.replyTo}
			if tt.to != "" {
				in.Recipients = []Recipient{{Session: tt.to, Explicit: !tt.group}}
			}
			got := tree.Deliveries(in)
			if len(got) != 1 || !reflect.DeepEqual(got[0].Reply, tt.want) || !got[0].Wake {
				t.Fatalf("got %+v; want %+v", got, tt.want)
			}
		})
	}
	for _, tt := range []struct {
		name  string
		tree  Tree
		input DeliveryInput
	}{
		{"self", tree, DeliveryInput{Event: contract.Event{From: session("child")}, Surface: contract.SurfaceFeed, Recipients: []Recipient{{Session: "child", Explicit: true}}}},
		{"group self", tree, DeliveryInput{Event: contract.Event{From: session("child")}, Surface: contract.SurfaceFeed, Recipients: []Recipient{{Session: "child"}}}},
		{"no root", NewTree(nil), DeliveryInput{Event: contract.Event{From: owner}, Surface: contract.SurfaceFeed}},
		{"empty mention", tree, DeliveryInput{Event: contract.Event{From: owner}, Surface: contract.SurfaceFeed, HasMentions: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tree.Deliveries(tt.input); len(got) != 0 {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestFinalReplies(t *testing.T) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: "child"}
	inputs := []contract.Delivery{{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: dm}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyPost, Target: parent}}}
	got := FinalReplies(inputs, map[Destination]contract.EventID{{Target: owner, Home: feed}: "post"}, nil)
	if len(got) != 3 || got[0].Posted != "post" || got[1].Reply.Home != dm || got[1].Posted != "" || got[2].Posted != "" {
		t.Fatalf("got %+v", got)
	}
}
