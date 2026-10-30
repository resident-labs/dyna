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

type FinalReply struct {
	Channel contract.ReplyChannel
	Wake    bool
	Posted  contract.EventID
}

// FinalReplies returns one response for each distinct final target and home.
func FinalReplies(inputs []contract.Delivery, posts map[contract.Principal]contract.EventID) []FinalReply {
	type destination struct {
		target contract.Principal
		home   contract.Conversation
	}
	seen := make(map[destination]bool)
	var replies []FinalReply
	for _, input := range inputs {
		channel := input.Reply
		key := destination{target: channel.Target, home: channel.Home}
		if channel.Kind != contract.ReplyFinal || seen[key] {
			continue
		}
		seen[key] = true
		post := posts[channel.Target]
		replies = append(replies, FinalReply{Channel: channel, Wake: post == "", Posted: post})
	}
	return replies
}
