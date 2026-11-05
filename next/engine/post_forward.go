package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// forwardQuote builds the post a forward sends from the post it quotes: the "Forwarded from" quote, then the sender's note.
// A session may quote a DM post only from its own room, and carries a post's files only from its own room.
func (s *Service) forwardQuote(ctx context.Context, caller contract.Principal, req contract.PostRequest) (contract.PostRequest, *contract.Error) {
	place, err := s.store.PostPlaceOf(ctx, req.Forward)
	if errors.Is(err, store.ErrNotFound) {
		return req, invalid("no post " + string(req.Forward))
	}
	if err != nil {
		return req, failure(err)
	}
	if caller.Kind == contract.PrincipalSession && place.DM != "" && place.Room != req.Room {
		return req, &contract.Error{Code: contract.ErrForbidden, Message: "a DM post can be forwarded only from your own room"}
	}
	event, err := s.store.Event(ctx, place.Seq)
	if err != nil {
		return req, failure(err)
	}
	body := event.Body.(contract.PostCreated)
	roomName, err := s.store.RoomNameOf(ctx, place.Room)
	if err != nil {
		return req, failure(err)
	}
	author := string(event.From.Kind)
	if event.From.Kind == contract.PrincipalSession {
		names, err := s.store.SessionNames(ctx, []contract.SessionID{event.From.Session})
		if err != nil {
			return req, failure(err)
		}
		author = names[event.From.Session]
	}
	text := body.Text
	outside := place.Room != req.Room
	source := roomName
	if body.Source != nil {
		outside, source, author = true, string(body.Source.RoomName), body.Source.SessionName
	}
	from := author
	if outside {
		from = author + " in " + source
		text = contract.Outside("room "+source, author, text)
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight("> "+line, " ")
	}
	quote := "> **Forwarded from " + from + "** · " + event.Time.UTC().Format("Jan 2, 15:04") + " UTC\n>\n" + strings.Join(lines, "\n")
	if strings.TrimSpace(req.Text) != "" {
		quote += "\n\n" + req.Text
	}
	req.Text = quote
	if place.Room == req.Room {
		req.Data = body.Data
		for _, file := range body.Attachments {
			file.Visual = nil
			req.Attachments = append(req.Attachments, file)
		}
	}
	return req, nil
}
