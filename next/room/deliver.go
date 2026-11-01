package room

import "github.com/resident-labs/dyna/contract"

// Recipient records whether an explicit mention, rather than a group, selected
// the session. Explicit mentions take precedence over group mentions.
type Recipient struct {
	Session  contract.SessionID
	Explicit bool
}

type DeliveryInput struct {
	Event       contract.Event
	Surface     contract.Surface
	Recipients  []Recipient
	HasMentions bool
	Home        contract.Conversation
	ReplyTo     contract.EventID
}

func (t Tree) Deliveries(in DeliveryInput) []contract.Delivery {
	recipients := in.Recipients
	if !in.HasMentions && len(recipients) == 0 && in.Surface == contract.SurfaceFeed && in.Event.From.Kind == contract.PrincipalOwner && t.root != "" {
		recipients = []Recipient{{Session: t.root, Explicit: true}}
	}
	var out []contract.Delivery
	seen := make(map[contract.SessionID]int)
	for _, r := range recipients {
		if in.Event.From.Kind == contract.PrincipalSession && r.Session == in.Event.From.Session {
			continue
		}
		if i, ok := seen[r.Session]; ok {
			if !r.Explicit {
				continue
			}
			out[i].Reply = t.reply(in, r)
			continue
		}
		seen[r.Session] = len(out)
		out = append(out, contract.Delivery{Seq: in.Event.Seq, To: contract.Principal{Kind: contract.PrincipalSession, Session: r.Session}, Surface: in.Surface, Reply: t.reply(in, r), Wake: true})
	}
	return out
}

func (t Tree) reply(in DeliveryInput, r Recipient) contract.ReplyChannel {
	reply := contract.ReplyChannel{Kind: contract.ReplyNone}
	if !r.Explicit || in.Surface == contract.SurfaceEngine || in.Surface == contract.SurfaceService {
		return reply
	}
	reply.Target = in.Event.From
	reply.Home = in.Home
	switch {
	case in.Surface == contract.SurfaceDM || in.Surface == contract.SurfaceCard || in.Event.From.Kind == contract.PrincipalOwner:
		reply.Kind = contract.ReplyFinal
		reply.ReplyTo = in.ReplyTo
		if in.Surface == contract.SurfaceFeed && reply.ReplyTo == "" {
			reply.ReplyTo = in.Event.ID
		}
	case in.Surface == contract.SurfaceSession && t.Above(r.Session, in.Event.From.Session):
		reply.Kind = contract.ReplyPost
	case t.nodes[r.Session].Parent == in.Event.From.Session:
		reply.Kind = contract.ReplyFinal
	default:
		reply.Kind = contract.ReplyPost
	}
	return reply
}

type Destination struct {
	Target contract.Principal
	Home   contract.Conversation
}

// PostsForReplies finds the latest post to each final destination in the turn.
func PostsForReplies(inputs []contract.Delivery, events []contract.Event, session contract.SessionID) map[Destination]contract.EventID {
	posts := make(map[Destination]contract.EventID)
	for _, event := range events {
		if event.From.Kind != contract.PrincipalSession || event.From.Session != session {
			continue
		}
		post := event.Body.(contract.PostCreated)
		for _, input := range inputs {
			channel := input.Reply
			if channel.Kind != contract.ReplyFinal || channel.Home != post.Home {
				continue
			}
			matched := channel.Target.Kind == contract.PrincipalOwner
			for _, mentioned := range post.Mentions {
				if mentioned == channel.Target {
					matched = true
				}
			}
			if matched {
				posts[Destination{Target: channel.Target, Home: channel.Home}] = event.ID
			}
		}
	}
	return posts
}

// FinalReplies returns one response for each distinct final target and home.
func FinalReplies(inputs []contract.Delivery, posts map[Destination]contract.EventID) []contract.FinalReply {
	seen := make(map[Destination]bool)
	var replies []contract.FinalReply
	for _, input := range inputs {
		channel := input.Reply
		key := Destination{Target: channel.Target, Home: channel.Home}
		if channel.Kind != contract.ReplyFinal || seen[key] {
			continue
		}
		seen[key] = true
		post := posts[key]
		replies = append(replies, contract.FinalReply{Reply: channel, Posted: post})
	}
	return replies
}
