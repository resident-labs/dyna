package room

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestDeliveries(t *testing.T) {
	tree := NewTree([]Node{{ID: "root"}, {ID: "parent", Parent: "root"}, {ID: "child", Parent: "parent"}, {ID: "sibling", Parent: "parent"}})
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	session := func(id contract.SessionID) contract.Principal {
		return contract.Principal{Kind: contract.PrincipalSession, Session: id}
	}
	for _, tt := range []struct {
		name    string
		from    contract.Principal
		surface contract.Surface
		to      contract.SessionID
		group   bool
		want    contract.ReplyKind
	}{
		{"owner feed", owner, contract.SurfaceFeed, "child", false, contract.ReplyFinal},
		{"owner unmentioned", owner, contract.SurfaceFeed, "", false, contract.ReplyFinal},
		{"owner dm", owner, contract.SurfaceDM, "child", false, contract.ReplyFinal},
		{"brief", session("parent"), contract.SurfaceSession, "child", false, contract.ReplyFinal},
		{"parent post", session("parent"), contract.SurfaceFeed, "child", false, contract.ReplyFinal},
		{"card", owner, contract.SurfaceCard, "child", false, contract.ReplyFinal},
		{"child result", session("child"), contract.SurfaceSession, "parent", false, contract.ReplyPost},
		{"sibling post", session("sibling"), contract.SurfaceFeed, "child", false, contract.ReplyPost},
		{"group", session("parent"), contract.SurfaceFeed, "child", true, contract.ReplyNone},
		{"engine", contract.Principal{Kind: contract.PrincipalEngine}, contract.SurfaceEngine, "child", false, contract.ReplyNone},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := DeliveryInput{Event: contract.Event{ID: "event", Seq: 1, From: tt.from}, Surface: tt.surface, Home: contract.Conversation{Kind: contract.ConversationFeed}}
			if tt.to != "" {
				in.Recipients = []Recipient{{Session: tt.to, Explicit: !tt.group}}
			}
			got := tree.Deliveries(in)
			if len(got) != 1 || got[0].Reply.Kind != tt.want || !got[0].Wake {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestFinalReplies(t *testing.T) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	inputs := []contract.Delivery{{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyPost, Target: parent}}}
	got := FinalReplies(inputs, map[contract.Principal]contract.EventID{owner: "post"})
	if len(got) != 2 || got[0].Wake || got[0].Posted != "post" || !got[1].Wake {
		t.Fatalf("got %+v", got)
	}
}
