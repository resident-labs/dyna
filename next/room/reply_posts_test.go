package room

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestPostsForReplies(t *testing.T) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: "child"}
	inputs := []contract.Delivery{{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: dm}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}}}
	events := []contract.Event{{ID: "first", From: contract.Principal{Kind: contract.PrincipalSession, Session: "child"}, Body: contract.PostCreated{Home: feed, Mentions: []contract.Principal{parent}}}, {ID: "latest", From: contract.Principal{Kind: contract.PrincipalSession, Session: "child"}, Body: contract.PostCreated{Home: feed}}, {ID: "other", From: parent, Body: contract.PostCreated{Home: dm}}}
	got := PostsForReplies(inputs, events, "child")
	if got[Destination{Target: owner, Home: feed}] != "latest" || got[Destination{Target: parent, Home: feed}] != "first" || got[Destination{Target: owner, Home: dm}] != "" {
		t.Fatalf("posts %+v", got)
	}
}

func TestPostsForRepliesMatchesHome(t *testing.T) {
	parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: "child"}
	inputs := []contract.Delivery{{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: dm}}}
	events := []contract.Event{{ID: "right", From: contract.Principal{Kind: contract.PrincipalSession, Session: "child"}, Body: contract.PostCreated{Home: feed, Mentions: []contract.Principal{parent}}}, {ID: "wrong", From: contract.Principal{Kind: contract.PrincipalSession, Session: "child"}, Body: contract.PostCreated{Home: dm, Mentions: []contract.Principal{parent}}}}
	got := PostsForReplies(inputs, events, "child")
	if got[Destination{Target: parent, Home: feed}] != "right" || got[Destination{Target: parent, Home: dm}] != "wrong" {
		t.Fatalf("posts %+v", got)
	}
}
