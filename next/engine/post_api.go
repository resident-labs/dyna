package engine

import (
	"context"
	"errors"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/room"
)

// postFile finds a blob for a post in a room. A session uses only what its room holds. The owner can forward a blob from
// another room, which hard-links it here: a blob never changes, so each room keeps its own link and neither depends on the other.
func (s *Service) postFile(ctx context.Context, caller contract.Principal, room contract.RoomID, hash, missing string) (os.FileInfo, *contract.Error) {
	path := s.layout.File(room, hash)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		from, found := s.layout.FindFile(hash)
		if !found {
			return nil, invalid(missing)
		}
		if caller.Kind != contract.PrincipalOwner {
			return nil, forbidden()
		}
		err = s.roomWrite(ctx, room, func() error {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				return err
			}
			if err := os.Link(from, path); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			return nil
		})
		if errors.Is(err, os.ErrNotExist) {
			return nil, invalid(missing)
		}
		if err != nil {
			return nil, failure(err)
		}
		info, err = os.Stat(path)
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, invalid(missing)
	}
	return info, nil
}

func (s *Service) Post(ctx context.Context, caller contract.Principal, req contract.PostRequest) (contract.PostResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.PostResponse{}, e
	}
	if len(req.ToolCallID) > 4096 || (req.ToolCallID != "" && caller.Kind != contract.PrincipalSession) {
		return contract.PostResponse{}, invalid("invalid tool call ID")
	}
	if len(req.ClientID) > 64 {
		return contract.PostResponse{}, invalid("clientId is longer than 64 characters")
	}
	// A repeat answers from the first post, before anything that can change since then (a closed session, a removed file).
	if req.ClientID != "" {
		id, seq, found, err := s.store.PostByClientID(ctx, req.Room, caller, req.ClientID)
		if err != nil {
			return contract.PostResponse{}, failure(err)
		}
		if found {
			return contract.PostResponse{Event: id, Seq: seq}, nil
		}
	}
	// A post carries text, files or both.
	if strings.TrimSpace(req.Text) == "" && len(req.Attachments) == 0 {
		return contract.PostResponse{}, invalid("post text or a file is required")
	}
	if len(req.Attachments) > 16 {
		return contract.PostResponse{}, invalid("too many attachments")
	}
	visuals := 0
	for i := range req.Attachments {
		file := &req.Attachments[i]
		if v := file.Visual; v != nil {
			visuals++
			if visuals > 1 || file.Media != "text/html" || strings.TrimSpace(req.Text) == "" || v.Height < 120 || v.Height > 1200 || len(v.Title) > 255 || len(v.Shots) > 16 || len(v.Checks) > 16 {
				return contract.PostResponse{}, invalid("invalid visual")
			}
			refs := []string{v.Banner, v.Thumbnail}
			for _, shot := range v.Shots {
				if shot.Width <= 0 {
					return contract.PostResponse{}, invalid("invalid visual width")
				}
				refs = append(refs, shot.File)
			}
			for _, check := range v.Checks {
				switch check.Name {
				case contract.VisualCheckRenders, contract.VisualCheckConsole, contract.VisualCheckWidths, contract.VisualCheckContrast, contract.VisualCheckNetwork:
				default:
					return contract.PostResponse{}, invalid("invalid visual check")
				}
				switch check.Status {
				case contract.VisualCheckPass, contract.VisualCheckWarn, contract.VisualCheckFail, contract.VisualCheckSkip:
				default:
					return contract.PostResponse{}, invalid("invalid visual status")
				}
			}
			for _, ref := range refs {
				if ref == "" {
					continue
				}
				if !validHash(ref) {
					return contract.PostResponse{}, invalid("invalid visual file")
				}
				if _, problem := s.postFile(ctx, caller, req.Room, ref, "visual file not found"); problem != nil {
					return contract.PostResponse{}, problem
				}
			}
		}
		media, _, mediaErr := mime.ParseMediaType(file.Media)
		if !validHash(file.SHA256) || file.Name == "" || len(file.Name) > 255 || mediaErr != nil {
			return contract.PostResponse{}, invalid("invalid attachment")
		}
		info, problem := s.postFile(ctx, caller, req.Room, file.SHA256, "attachment not found")
		if problem != nil {
			return contract.PostResponse{}, problem
		}
		file.Bytes = info.Size()
		file.Media = media
	}
	if req.VisualReply != "" && (req.VisualUpdate != "" || caller.Kind != contract.PrincipalOwner || len(req.Mentions) != 0 || req.ReplyTo != "") {
		return contract.PostResponse{}, invalid("invalid visual reply")
	}
	if req.VisualUpdate != "" && visuals != 1 {
		return contract.PostResponse{}, invalid("visual update requires one visual")
	}
	if req.Home.Kind == "" {
		req.Home.Kind = contract.ConversationFeed
	}
	surface := contract.SurfaceFeed
	if req.Home.Kind == contract.ConversationDM {
		if caller.Kind != contract.PrincipalOwner || req.Home.Session == "" || len(req.Mentions) != 0 {
			return contract.PostResponse{}, invalid("invalid DM")
		}
		session, err := s.store.Session(ctx, req.Home.Session)
		if err != nil {
			return contract.PostResponse{}, failure(err)
		}
		if session.Room != req.Room {
			return contract.PostResponse{}, invalid("DM session is in another room")
		}
		surface = contract.SurfaceDM
	} else if req.Home.Kind != contract.ConversationFeed || req.Home.Session != "" {
		return contract.PostResponse{}, invalid("invalid conversation")
	}
	tree, err := s.store.RoomTree(ctx, req.Room)
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	var mentions []contract.Principal
	var recipients []room.Recipient
	seen := make(map[contract.SessionID]bool)
	for _, name := range req.Mentions {
		if !strings.HasPrefix(name, "@") {
			name = "@" + name
		}
		address, err := room.ParseAddress(name)
		if err != nil {
			return contract.PostResponse{}, invalid(err.Error())
		}
		names := make(map[string]contract.SessionID)
		if address.Kind == room.AddressName {
			targetRoom := req.Room
			if address.Room != "" {
				targetRoom, err = s.store.RoomByName(ctx, address.Room)
				if errors.Is(err, store.ErrNotFound) {
					return contract.PostResponse{}, invalid("unknown room")
				}
				if err != nil {
					return contract.PostResponse{}, failure(err)
				}
			}
			session, err := s.store.SessionByName(ctx, targetRoom, address.Name)
			if errors.Is(err, store.ErrNotFound) {
				return contract.PostResponse{}, invalid("unknown session @" + address.Name)
			}
			if err != nil {
				return contract.PostResponse{}, failure(err)
			}
			names[address.Name] = session.ID
		}
		ids, group := tree.Resolve(caller.Session, address, names)
		for _, target := range ids {
			recipients = append(recipients, room.Recipient{Session: target, Explicit: !group})
			if !seen[target] {
				mentions = append(mentions, contract.Principal{Kind: contract.PrincipalSession, Session: target})
				seen[target] = true
			}
		}
	}
	if surface == contract.SurfaceDM {
		recipients = append(recipients, room.Recipient{Session: req.Home.Session, Explicit: true})
	}
	event, err := s.event(req.Room, caller, contract.PostCreated{Text: req.Text, Mentions: mentions, Home: req.Home, ReplyTo: req.ReplyTo, Attachments: req.Attachments, ClientID: req.ClientID, ToolCallID: req.ToolCallID})
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	deliveries := tree.Deliveries(room.DeliveryInput{Event: event, Surface: surface, Recipients: recipients, HasMentions: len(req.Mentions) > 0, Home: req.Home, ReplyTo: req.ReplyTo})
	id, seq, err := s.store.PostOnce(ctx, event, deliveries, req.VisualUpdate, req.VisualReply)
	var superseded *store.VisualSupersededError
	if errors.As(err, &superseded) {
		return contract.PostResponse{}, &contract.Error{Code: contract.ErrVisualSuperseded, Message: "visual has a newer version", Visual: &contract.VisualSuperseded{Event: superseded.Event, Version: superseded.Version, Seq: superseded.Seq}}
	}
	if errors.Is(err, store.ErrVisualNoAuthor) {
		return contract.PostResponse{}, invalid("visual has no session author")
	}
	if errors.Is(err, store.ErrVisualForbidden) {
		return contract.PostResponse{}, forbidden()
	}
	if errors.Is(err, store.ErrNotFound) {
		return contract.PostResponse{}, invalid("visual not found")
	}
	if err != nil {
		return contract.PostResponse{}, failure(err)
	}
	return contract.PostResponse{Event: id, Seq: seq}, nil
}

func (s *Service) EventsList(ctx context.Context, caller contract.Principal, req contract.EventsListRequest) (contract.EventsListResponse, *contract.Error) {
	if e := s.roomAccess(ctx, caller, req.Room); e != nil {
		return contract.EventsListResponse{}, e
	}
	if req.Limit == 0 {
		req.Limit = 100
	}
	if req.Limit < 0 || req.AfterSeq < 0 || req.BeforeSeq < 0 || (req.BeforeSeq > 0 && req.AfterSeq > 0) {
		return contract.EventsListResponse{}, invalid("invalid event range")
	}
	var dm contract.SessionID
	if req.Home != nil {
		if req.Home.Kind != contract.ConversationDM || req.Home.Session == "" {
			return contract.EventsListResponse{}, invalid("home must be a DM")
		}
		dm = req.Home.Session
	}
	var events []contract.Event
	var err error
	if req.Event != "" {
		if req.AfterSeq != 0 || req.BeforeSeq != 0 || req.Home != nil || len(req.Types) != 0 {
			return contract.EventsListResponse{}, invalid("event lookup cannot have filters")
		}
		var event contract.Event
		event, err = s.store.EventInRoom(ctx, req.Room, req.Event)
		if err == nil {
			events = append(events, event)
		} else if errors.Is(err, store.ErrNotFound) {
			err = nil
		}
	} else {
		events, err = s.store.EventsPage(ctx, req.Room, req.AfterSeq, req.BeforeSeq, req.Limit, req.Types, dm)
	}
	if err != nil {
		return contract.EventsListResponse{}, failure(err)
	}
	next := req.AfterSeq
	if len(events) > 0 {
		next = events[len(events)-1].Seq
	}
	// Names of the sessions this page names, so that posts and events of closed sessions keep their authors.
	named := map[contract.SessionID]bool{}
	for _, event := range events {
		named[event.From.Session] = true
		switch body := event.Body.(type) {
		case contract.SessionStarted:
			named[body.Session] = true
		case contract.SessionClosed:
			named[body.Session] = true
		case contract.TurnStarted:
			named[body.Session] = true
		case contract.TurnEnded:
			named[body.Session] = true
		case contract.ShellJobEnded:
			named[body.Session] = true
		case contract.BoxChanged:
			named[body.Session] = true
		case contract.PrOpened:
			named[body.Session] = true
		case contract.PrChanged:
			named[body.Session] = true
		case contract.IssueChanged:
			named[body.Session] = true
		case contract.OpRequested:
			named[body.By.Session] = true
		case contract.QuestionAnswered:
			named[body.Session] = true
		case contract.QuestionWithdrawn:
			named[body.Session] = true
		}
	}
	delete(named, "")
	ids := make([]contract.SessionID, 0, len(named))
	for id := range named {
		ids = append(ids, id)
	}
	names, err := s.store.SessionNames(ctx, ids)
	if err != nil {
		return contract.EventsListResponse{}, failure(err)
	}
	return contract.EventsListResponse{Events: events, Next: next, Names: names}, nil
}
