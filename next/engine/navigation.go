package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness/pig"
)

// navScope says whose room the navigation tools keep DMs, transcripts and files to. The owner has no room and no limit;
// a session has its own; any other caller is refused.
func (s *Service) navScope(ctx context.Context, caller contract.Principal) (own contract.RoomID, owner bool, e *contract.Error) {
	switch caller.Kind {
	case contract.PrincipalOwner:
		return "", true, nil
	case contract.PrincipalSession:
		session, err := s.store.Session(ctx, caller.Session)
		if err != nil {
			return "", false, failure(err)
		}
		return session.Room, false, nil
	}
	return "", false, forbidden()
}

// navRoom resolves a room reference: empty is all rooms, here the caller's, otherwise a name or ID.
func (s *Service) navRoom(ctx context.Context, ref string, own contract.RoomID, owner bool) (contract.RoomID, *contract.Error) {
	switch {
	case ref == "":
		return "", nil
	case ref == contract.SearchRoomHere && !owner:
		return own, nil
	case ref == contract.SearchRoomHere:
		return "", invalid("the owner has no room: name one")
	}
	id, _, err := s.store.ResolveRoom(ctx, ref)
	if errors.Is(err, store.ErrNotFound) {
		return "", invalid("unknown room " + ref)
	}
	if err != nil {
		return "", failure(err)
	}
	return id, nil
}

// navSearch checks what the three searches share and returns the words, the room, the limit and the cursor.
func (s *Service) navSearch(ctx context.Context, caller contract.Principal, req contract.SearchRequest, needQuery bool) (words []string, room contract.RoomID, own contract.RoomID, owner bool, limit int, cursor int64, e *contract.Error) {
	own, owner, e = s.navScope(ctx, caller)
	if e != nil {
		return
	}
	words = strings.Fields(req.Query)
	if len(req.Query) > contract.SearchQueryCharsMax || len(words) > contract.SearchWordsMax || (needQuery && len(words) == 0) {
		e = invalid("query is 1 to 8 words and at most 200 characters")
		return
	}
	limit = req.Limit
	if limit < 0 || limit > contract.SearchLimitMax {
		e = invalid("limit is at most 25")
		return
	}
	if limit == 0 {
		limit = contract.SearchLimitDefault
	}
	if req.After != "" {
		var err error
		if cursor, err = strconv.ParseInt(req.After, 10, 64); err != nil || cursor <= 0 || cursor > 1<<40 {
			e = invalid("after is a result's next")
			return
		}
	}
	room, e = s.navRoom(ctx, req.Room, own, owner)
	return
}

// clipBytes cuts text to at most max bytes, at a character boundary.
func clipBytes(text string, max int) (string, bool) {
	if len(text) <= max {
		return text, false
	}
	for max > 0 && !utf8.RuneStart(text[max]) {
		max--
	}
	return text[:max], true
}

// fitPage drops hits from the end of a search result until its JSON is at most NavPageBytes, and returns how many it keeps.
// One hit always stays.
func fitPage(result *contract.SearchResponse) int {
	for {
		encoded, _ := json.Marshal(result)
		switch {
		case len(encoded) <= contract.NavPageBytes:
			return max(len(result.Posts), len(result.Sessions), len(result.Rooms))
		case len(result.Posts) > 1:
			result.Posts = result.Posts[:len(result.Posts)-1]
		case len(result.Sessions) > 1:
			result.Sessions = result.Sessions[:len(result.Sessions)-1]
		case len(result.Rooms) > 1:
			result.Rooms = result.Rooms[:len(result.Rooms)-1]
		default:
			return 1
		}
	}
}

func clipRunes(text string, max int) (string, bool) {
	if utf8.RuneCountInString(text) <= max {
		return text, false
	}
	return string([]rune(text)[:max]), true
}

// SearchPosts searches the text of posts. Feed posts are searched in every room; a DM post only in the caller's own room.
func (s *Service) SearchPosts(ctx context.Context, caller contract.Principal, req contract.SearchRequest) (contract.SearchResponse, *contract.Error) {
	words, room, own, owner, limit, cursor, e := s.navSearch(ctx, caller, req, true)
	if e != nil {
		return contract.SearchResponse{}, e
	}
	for i, word := range words {
		words[i] = `"` + strings.ReplaceAll(word, `"`, `""`) + `"*`
	}
	hits, total, err := s.store.SearchPostHits(ctx, store.PostHitQuery{Match: strings.Join(words, " AND "), Room: room, DMRoom: own, AllDMs: owner, From: req.From, Before: cursor, Limit: limit + 1})
	if err != nil {
		return contract.SearchResponse{}, failure(err)
	}
	var ownName string
	if !owner {
		if ownName, err = s.store.RoomNameOf(ctx, own); err != nil {
			return contract.SearchResponse{}, failure(err)
		}
	}
	more := len(hits) > limit
	hits = hits[:min(len(hits), limit)]
	result := contract.SearchResponse{Total: total}
	for _, hit := range hits {
		out := contract.SearchHit{ID: hit.ID, Room: hit.RoomName, Home: "feed", From: hit.Author, At: hit.At, Files: hit.Files}
		if hit.DM != "" {
			out.Home = "dm:" + hit.DM
		}
		source, author := hit.RoomName, hit.Author
		if hit.Source != nil {
			out.From = hit.Source.SessionName + " in " + string(hit.Source.RoomName)
			source, author = string(hit.Source.RoomName), hit.Source.SessionName
		}
		out.Snippet, _ = clipRunes(hit.Snippet, contract.SearchSnippetChars)
		// Text of another room, or delivered from one, is outside text to an agent. The owner reads it plain.
		if !owner && (hit.RoomName != ownName || hit.Source != nil) {
			out.Snippet = contract.Outside("room "+source, author, out.Snippet)
		}
		result.Posts = append(result.Posts, out)
	}
	kept := fitPage(&result)
	if more || kept < len(hits) {
		result.Next = strconv.FormatInt(hits[kept-1].Seq, 10)
	}
	return result, nil
}

// SearchSessions finds sessions by their name or goal, in every room.
func (s *Service) SearchSessions(ctx context.Context, caller contract.Principal, req contract.SearchRequest) (contract.SearchResponse, *contract.Error) {
	words, room, own, owner, limit, offset, e := s.navSearch(ctx, caller, req, true)
	if e != nil {
		return contract.SearchResponse{}, e
	}
	hits, total, err := s.store.SearchSessionHits(ctx, words, room, int(offset), limit)
	if err != nil {
		return contract.SearchResponse{}, failure(err)
	}
	var ownName string
	if !owner {
		if ownName, err = s.store.RoomNameOf(ctx, own); err != nil {
			return contract.SearchResponse{}, failure(err)
		}
	}
	for i := range hits {
		hits[i].Goal, _ = clipRunes(hits[i].Goal, contract.SearchGoalChars)
		// A goal is free text of the room it came from: outside text to an agent of another room. Names are not.
		if !owner && hits[i].Goal != "" && hits[i].Room != ownName {
			hits[i].Goal = contract.Outside("room "+hits[i].Room, hits[i].Name, hits[i].Goal)
		}
	}
	result := contract.SearchResponse{Sessions: hits, Total: total}
	if kept := fitPage(&result); int(offset)+kept < total {
		result.Next = strconv.Itoa(int(offset) + kept)
	}
	return result, nil
}

// SearchRooms lists the rooms.
func (s *Service) SearchRooms(ctx context.Context, caller contract.Principal, req contract.SearchRequest) (contract.SearchResponse, *contract.Error) {
	words, room, _, _, limit, offset, e := s.navSearch(ctx, caller, req, false)
	if e != nil {
		return contract.SearchResponse{}, e
	}
	hits, total, err := s.store.SearchRoomHits(ctx, words, room, int(offset), limit)
	if err != nil {
		return contract.SearchResponse{}, failure(err)
	}
	result := contract.SearchResponse{Rooms: hits, Total: total}
	if kept := fitPage(&result); int(offset)+kept < total {
		result.Next = strconv.Itoa(int(offset) + kept)
	}
	return result, nil
}

// HistoryRead reads posts of one conversation. A feed is readable in any room; a DM only in the caller's own room.
func (s *Service) HistoryRead(ctx context.Context, caller contract.Principal, req contract.HistoryReadRequest) (contract.HistoryReadResponse, *contract.Error) {
	own, owner, e := s.navScope(ctx, caller)
	if e != nil {
		return contract.HistoryReadResponse{}, e
	}
	limit := req.Limit
	if limit < 0 || limit > contract.HistoryLimitMax {
		return contract.HistoryReadResponse{}, invalid("limit is at most 50")
	}
	if limit == 0 {
		limit = contract.HistoryLimitDefault
	}
	named := 0
	for _, id := range []contract.EventID{req.Around, req.Before, req.After, req.Post} {
		if id != "" {
			named++
		}
	}
	if named > 1 {
		return contract.HistoryReadResponse{}, invalid("name one of around, before, after and post")
	}
	var room contract.RoomID
	var dm contract.SessionID
	var anchor int64
	if id := cmpOr(req.Post, req.Around); id != "" {
		if req.Room != "" || req.DM != "" {
			return contract.HistoryReadResponse{}, invalid("a post names its own room and conversation")
		}
		place, err := s.store.PostPlaceOf(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return contract.HistoryReadResponse{}, invalid("no post " + string(id))
		}
		if err != nil {
			return contract.HistoryReadResponse{}, failure(err)
		}
		room, dm, anchor = place.Room, place.DM, place.Seq
		if dm != "" && !owner && room != own {
			return contract.HistoryReadResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "a DM can be read only in your own room"}
		}
	} else {
		if req.Room == "" && owner {
			return contract.HistoryReadResponse{}, invalid("the owner has no room: name one")
		}
		room = own
		if req.Room != "" {
			var e *contract.Error
			if room, e = s.navRoom(ctx, req.Room, own, owner); e != nil {
				return contract.HistoryReadResponse{}, e
			}
		}
		if req.DM != "" {
			if !owner && room != own {
				return contract.HistoryReadResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "a DM can be read only in your own room"}
			}
			id, err := s.store.SessionInRoom(ctx, room, req.DM)
			if errors.Is(err, store.ErrNotFound) {
				return contract.HistoryReadResponse{}, invalid("no session " + req.DM + " in that room")
			}
			if err != nil {
				return contract.HistoryReadResponse{}, failure(err)
			}
			dm = id
		}
		if cursor := cmpOr(req.Before, req.After); cursor != "" {
			place, err := s.store.PostPlaceOf(ctx, cursor)
			if errors.Is(err, store.ErrNotFound) || (err == nil && (place.Room != room || place.DM != dm)) {
				return contract.HistoryReadResponse{}, invalid("no post " + string(cursor) + " in this conversation")
			}
			if err != nil {
				return contract.HistoryReadResponse{}, failure(err)
			}
			anchor = place.Seq
		}
	}
	var events []contract.Event
	var err error
	focus := -1 // the post the page keeps whole when it must shrink; -1 is the newest end
	switch {
	case req.Post != "":
		var event contract.Event
		if event, err = s.store.Event(ctx, anchor); err == nil {
			events = []contract.Event{event}
		}
	case req.Around != "":
		var older, newer []contract.Event
		if older, err = s.store.PostsPage(ctx, room, dm, 0, anchor+1, (limit+1)/2); err == nil {
			if newer, err = s.store.PostsPage(ctx, room, dm, anchor, 0, limit/2); err == nil {
				events, focus = append(older, newer...), len(older)-1
			}
		}
	case req.After != "":
		events, err = s.store.PostsPage(ctx, room, dm, anchor, 0, limit)
		focus = 0
	default:
		events, err = s.store.PostsPage(ctx, room, dm, 0, anchor, limit)
	}
	if err != nil {
		return contract.HistoryReadResponse{}, failure(err)
	}
	ids := map[contract.SessionID]bool{dm: dm != ""}
	for _, event := range events {
		ids[event.From.Session] = true
		for _, mention := range event.Body.(contract.PostCreated).Mentions {
			ids[mention.Session] = true
		}
	}
	delete(ids, "")
	names, err := s.store.SessionNames(ctx, slices.Collect(func(yield func(contract.SessionID) bool) {
		for id := range ids {
			if !yield(id) {
				return
			}
		}
	}))
	if err != nil {
		return contract.HistoryReadResponse{}, failure(err)
	}
	roomName, err := s.store.RoomNameOf(ctx, room)
	if err != nil {
		return contract.HistoryReadResponse{}, failure(err)
	}
	result := contract.HistoryReadResponse{Room: roomName, Home: "feed", Posts: []contract.HistoryPost{}}
	if dm != "" {
		result.Home = "dm:" + names[dm]
	}
	seqs := make([]int64, len(events))
	for i, event := range events {
		body := event.Body.(contract.PostCreated)
		post := contract.HistoryPost{ID: event.ID, At: event.Time, From: string(event.From.Kind), ReplyTo: body.ReplyTo}
		if event.From.Kind == contract.PrincipalSession {
			post.From = names[event.From.Session]
		}
		source, author := roomName, post.From
		if body.Source != nil {
			post.From = body.Source.SessionName + " in " + string(body.Source.RoomName)
		}
		for _, mention := range body.Mentions {
			post.To = append(post.To, names[mention.Session])
		}
		if req.Post != "" {
			post.Text, post.Cut = clipBytes(body.Text, contract.HistoryPostBytes)
		} else {
			post.Text, post.Cut = clipRunes(body.Text, contract.HistoryTextChars)
		}
		if body.Source != nil {
			source, author = string(body.Source.RoomName), body.Source.SessionName
		}
		// Text of another room, or delivered from one, is outside text to an agent. The owner reads it plain.
		if !owner && (room != own || body.Source != nil) {
			post.Text = contract.Outside("room "+source, author, post.Text)
		}
		for _, file := range body.Attachments {
			post.Files = append(post.Files, fmt.Sprintf("%s (%s, %d KB)", file.Name, file.Media, (file.Bytes+1023)/1024))
		}
		result.Posts = append(result.Posts, post)
		seqs[i] = event.Seq
	}
	if req.Post != "" {
		return result, nil
	}
	if focus < 0 {
		focus = len(result.Posts) - 1
	}
	// A page past 8 KB loses the posts farthest from the one the reader asked for, older first on a tie.
	for len(result.Posts) > 1 {
		encoded, _ := json.Marshal(result)
		if len(encoded) <= contract.NavPageBytes {
			break
		}
		if focus >= len(result.Posts)-1-focus {
			result.Posts, seqs, focus = result.Posts[1:], seqs[1:], focus-1
		} else {
			result.Posts, seqs = result.Posts[:len(result.Posts)-1], seqs[:len(seqs)-1]
		}
	}
	if len(seqs) == 0 {
		return result, nil
	}
	if more, err := s.store.PostBeyond(ctx, room, dm, seqs[0], false); err != nil {
		return contract.HistoryReadResponse{}, failure(err)
	} else if more {
		result.Older = result.Posts[0].ID
	}
	if more, err := s.store.PostBeyond(ctx, room, dm, seqs[len(seqs)-1], true); err != nil {
		return contract.HistoryReadResponse{}, failure(err)
	} else if more {
		result.Newer = result.Posts[len(result.Posts)-1].ID
	}
	return result, nil
}

func cmpOr(a, b contract.EventID) contract.EventID {
	if a != "" {
		return a
	}
	return b
}

// TranscriptDigest reads a session's turns as short lines. It reaches only sessions of the caller's own room: a session
// named by an ID of another room is refused, and a name is found only in the caller's room. The owner reaches any.
func (s *Service) TranscriptDigest(ctx context.Context, caller contract.Principal, req contract.TranscriptDigestRequest) (contract.TranscriptDigestResponse, *contract.Error) {
	own, owner, e := s.navScope(ctx, caller)
	if e != nil {
		return contract.TranscriptDigestResponse{}, e
	}
	limit := req.Limit
	if limit < 0 || limit > contract.TranscriptLimitMax || req.Offset < 0 || req.Session == "" || (req.Item != "" && req.Turn == "") || (req.Turn != "" && req.Before != "") {
		return contract.TranscriptDigestResponse{}, invalid("invalid transcript request: limit is at most 5, and item needs turn")
	}
	if limit == 0 {
		limit = contract.TranscriptLimitDef
	}
	session := contract.SessionID(req.Session)
	room, err := s.store.TranscriptRoom(ctx, session)
	if errors.Is(err, store.ErrNotFound) && !owner {
		if session, err = s.store.SessionInRoom(ctx, own, req.Session); err == nil {
			room = own
		}
	}
	if errors.Is(err, store.ErrNotFound) {
		return contract.TranscriptDigestResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: "session not found"}
	}
	if err != nil {
		return contract.TranscriptDigestResponse{}, failure(err)
	}
	if !owner && room != own {
		return contract.TranscriptDigestResponse{}, &contract.Error{Code: contract.ErrForbidden, Message: "a transcript can be read only in your own room"}
	}
	roomName, err := s.store.RoomNameOf(ctx, room)
	if err != nil {
		return contract.TranscriptDigestResponse{}, failure(err)
	}
	read, e := s.TranscriptRead(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.TranscriptReadRequest{Session: session, Turn: req.Turn, Before: req.Before, Limit: limit, Item: req.Item, Part: contract.TranscriptPartText, Offset: req.Offset})
	if e != nil {
		return contract.TranscriptDigestResponse{}, e
	}
	names, err := s.store.SessionNames(ctx, []contract.SessionID{session})
	if err != nil {
		return contract.TranscriptDigestResponse{}, failure(err)
	}
	result := contract.TranscriptDigestResponse{Session: names[session], Room: roomName}
	if req.Item != "" {
		result.Chunk, result.NextOffset = read.Chunk, read.NextOffset
		return result, nil
	}
	sessions := map[contract.SessionID]bool{}
	for _, turn := range read.Turns {
		for _, input := range turn.Inputs {
			sessions[input.From.Session] = true
		}
		for _, item := range turn.Items {
			if item.From != nil {
				sessions[item.From.Session] = true
			}
		}
	}
	delete(sessions, "")
	if names, err = s.store.SessionNames(ctx, slices.Collect(func(yield func(contract.SessionID) bool) {
		for id := range sessions {
			if !yield(id) {
				return
			}
		}
	})); err != nil {
		return contract.TranscriptDigestResponse{}, failure(err)
	}
	author := func(from contract.Principal) string {
		if from.Kind == contract.PrincipalSession {
			return names[from.Session]
		}
		return string(from.Kind)
	}
	for _, turn := range read.Turns {
		out := contract.DigestTurn{ID: turn.ID, Started: turn.Started, In: []string{}, Steps: []string{}, Outcome: "running"}
		switch turn.Outcome {
		case contract.TurnCompleted:
			out.Outcome = "ok"
		case contract.TurnFailed:
			out.Outcome = "failed"
		case contract.TurnStopped:
			out.Outcome = "stopped"
		}
		for _, input := range turn.Inputs {
			line, cut := clipRunes(author(input.From)+": "+input.Text, contract.TranscriptInputChars)
			out.In = append(out.In, line)
			if cut || input.More {
				out.Cut = append(out.Cut, "input:"+string(input.ID))
			}
		}
		step := func(text string) {
			line, _ := clipRunes(text, contract.TranscriptStepChars)
			out.Steps = append(out.Steps, line)
		}
		finalID, finalCut := "", false
		for _, item := range turn.Items {
			switch item.Kind {
			case contract.TranscriptSteer:
				line, cut := clipRunes(author(*item.From)+": "+item.Text, contract.TranscriptInputChars)
				out.In = append(out.In, line)
				if cut || item.More {
					out.Cut = append(out.Cut, item.ID)
				}
			case contract.TranscriptTool:
				label := pig.TranscriptStep(item).Label
				if label == "" {
					label = item.Tool
				}
				status := " — ok"
				switch {
				case item.Failed:
					status = " — error"
				case item.Ended == nil:
					status = " — running"
				case item.Ended.Sub(item.At).Seconds() >= 1:
					status += fmt.Sprintf(" %ds", int(item.Ended.Sub(item.At).Seconds()))
				}
				step(label + status)
				for _, post := range item.Posts {
					step("post: " + post.Text)
				}
			case contract.TranscriptPost:
				step("post: " + item.Text)
			case contract.TranscriptText:
				out.Text, finalCut = clipRunes(item.Text, contract.TranscriptTextChars)
				finalID = item.ID
			case contract.TranscriptTooLarge:
				step("(an item too large to show)")
			}
		}
		if finalCut {
			out.Cut = append(out.Cut, finalID)
		}
		result.Turns = append(result.Turns, out)
	}
	if read.More {
		result.Next = read.Before
	}
	// A page past 8 KB loses its oldest turns, then its oldest steps; the cursor names the last turn it kept.
	for {
		encoded, _ := json.Marshal(result)
		if len(encoded) <= contract.NavPageBytes {
			break
		}
		if len(result.Turns) > 1 {
			result.Turns = result.Turns[:len(result.Turns)-1]
			result.Next = result.Turns[len(result.Turns)-1].ID
			continue
		}
		if steps := result.Turns[0].Steps; len(steps) > 1 {
			result.Turns[0].Steps = steps[1:]
			continue
		}
		break
	}
	return result, nil
}
