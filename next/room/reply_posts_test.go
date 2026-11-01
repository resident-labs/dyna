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

func TestAPostToOthersDoesNotAnswerTheOwner(t *testing.T) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	lead := contract.Principal{Kind: contract.PrincipalSession, Session: "lead"}
	root := contract.Principal{Kind: contract.PrincipalSession, Session: "root"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	inputs := []contract.Delivery{{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed}}}
	events := []contract.Event{{ID: "to-lead", From: root, Body: contract.PostCreated{Home: feed, Mentions: []contract.Principal{lead}}}}
	if got := PostsForReplies(inputs, events, "root"); got[Destination{Target: owner, Home: feed}] != "" {
		t.Fatalf("a post to a lead answered the owner: %+v", got)
	}
	events = append(events, contract.Event{ID: "to-owner", From: root, Body: contract.PostCreated{Home: feed}})
	if got := PostsForReplies(inputs, events, "root"); got[Destination{Target: owner, Home: feed}] != "to-owner" {
		t.Fatalf("a plain post did not answer the owner: %+v", got)
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

func TestFinalRepliesLinkEndPosts(t *testing.T) {
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	parent := contract.Principal{Kind: contract.PrincipalSession, Session: "parent"}
	feed := contract.Conversation{Kind: contract.ConversationFeed}
	dm := contract.Conversation{Kind: contract.ConversationDM, Session: "child"}
	inputs := []contract.Delivery{{Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: feed}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: owner, Home: dm}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyFinal, Target: parent, Home: feed}}, {Reply: contract.ReplyChannel{Kind: contract.ReplyPost, Target: parent}}}
	posts := map[Destination]contract.EventID{{Target: owner, Home: feed}: "earlier update", {Target: owner, Home: dm}: "first end"}
	got := FinalReplies(inputs, posts, []contract.EventID{"first end", "last end"})
	if len(got) != 3 || got[0].Posted != "last end" || got[1].Posted != "first end" || got[2].Posted != "last end" {
		t.Fatalf("got %+v", got)
	}
}
