package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

func (s *Service) framePrincipal(ctx context.Context, viewer contract.SessionID, principal contract.Principal) (string, error) {
	switch principal.Kind {
	case contract.PrincipalOwner:
		return "the owner", nil
	case contract.PrincipalEngine:
		return "the Dyna engine", nil
	case contract.PrincipalSession:
		name, parent, viewerParent, err := s.store.FrameSession(ctx, viewer, principal.Session)
		if err != nil {
			return "", err
		}
		name = fmt.Sprintf("%q", name)
		if principal.Session == viewerParent {
			name = "your parent " + name
		} else if parent == viewer {
			name = "your child " + name
		}
		return name, nil
	}
	return "the Dyna engine", nil
}

func (s *Service) inputFrame(ctx context.Context, viewer contract.SessionID, event contract.Event, delivery contract.Delivery) (string, error) {
	sender, err := s.framePrincipal(ctx, viewer, event.From)
	if err != nil {
		return "", err
	}
	// A Run's post comes through the engine, but a script wrote it, so the frame must not vouch for it.
	if post, ok := event.Body.(contract.PostCreated); ok && post.Hook != "" {
		sender = fmt.Sprintf("the script of subscription %s", post.Hook)
	}
	var surface string
	switch delivery.Surface {
	case contract.SurfaceFeed:
		surface = "in the room feed"
	case contract.SurfaceDM:
		surface = "in your DM"
	case contract.SurfaceSession:
		surface = "as a session brief or result"
	case contract.SurfaceCard:
		surface = "through a card"
	case contract.SurfaceEngine:
		surface = "as a system notice"
	case contract.SurfaceService:
		surface = "in the room feed"
	}
	frame := fmt.Sprintf("[Dyna] From %s, %s.", sender, surface)
	if delivery.HandoffFrom != "" {
		origin, err := s.framePrincipal(ctx, viewer, contract.Principal{Kind: contract.PrincipalSession, Session: delivery.HandoffFrom})
		if err != nil {
			return "", err
		}
		frame += fmt.Sprintf(" This input was handed over when %s closed.", origin)
	}
	if post, ok := event.Body.(contract.PostCreated); ok && len(post.Attachments) > 0 {
		names := make([]string, 0, len(post.Attachments))
		for _, file := range post.Attachments {
			names = append(names, fmt.Sprintf("%q", file.Name))
		}
		frame += " Attached: " + strings.Join(names, ", ") + "."
	}
	switch delivery.Reply.Kind {
	case contract.ReplyFinal:
		target, err := s.framePrincipal(ctx, viewer, delivery.Reply.Target)
		if err != nil {
			return "", err
		}
		home := "in the feed"
		if delivery.Reply.Home.Kind == contract.ConversationDM {
			home = "in your DM"
		}
		frame += fmt.Sprintf(" Your final response goes to %s %s.", target, home)
	case contract.ReplyPost:
		frame += fmt.Sprintf(" No reply is delivered. Run dyna post only if %s must act.", sender)
	case contract.ReplyNone:
		frame += " No reply is expected. End your turn with one line."
	}
	return frame, nil
}
