package store

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// An endTurn post ends the turn whatever it owes and wherever the post goes. A final reply owed to the owner links the
// post, so nothing is sent twice; a post or none channel owes nothing.
func TestEndPostEndsEveryKindOfTurn(t *testing.T) {
	owed := map[string]contract.ReplyChannel{
		"final in DM":   {Kind: contract.ReplyFinal, Target: endTurnOwner, Home: endTurnDM},
		"final in feed": {Kind: contract.ReplyFinal, Target: endTurnOwner, Home: endTurnFeed},
		"post":          {Kind: contract.ReplyPost, Target: endTurnParent, Home: endTurnFeed},
		"none":          {Kind: contract.ReplyNone},
	}
	for name, channel := range owed {
		for _, home := range []contract.Conversation{endTurnDM, endTurnFeed} {
			s, post := endTurnFixture(t, []contract.ReplyChannel{channel}, contract.PostCreated{Text: "report", Home: home})
			body, _ := endChildTurn(t, s, contract.TurnOutputRequest{Outcome: contract.TurnCompleted, EndPosts: []contract.EventID{post.ID}})
			want := 0
			if channel.Kind == contract.ReplyFinal {
				want = 1
			}
			if body.Final != "" || len(body.Replies) != want || (want == 1 && body.Replies[0].Posted != post.ID) {
				t.Fatalf("%s, post in %s: %+v", name, home.Kind, body)
			}
			var open int
			if err := s.Read().QueryRowContext(context.Background(), "SELECT COUNT(*) FROM turns WHERE session='child' AND ended_at IS NULL").Scan(&open); err != nil || open != 0 {
				t.Fatalf("%s, post in %s: %d open turns, %v", name, home.Kind, open, err)
			}
			if got := parentDeliveries(t, s); len(got) != 0 {
				t.Fatalf("%s, post in %s: parent got %v", name, home.Kind, got)
			}
		}
	}
}
