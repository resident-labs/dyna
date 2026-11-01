package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pi"
	"github.com/resident-labs/dyna/room"
)

const maxTranscriptCallPosts = 8

// TranscriptRead reads the acknowledged prefix without holding the writer or service lock.
func (s *Service) TranscriptRead(ctx context.Context, caller contract.Principal, req contract.TranscriptReadRequest) (contract.TranscriptReadResponse, *contract.Error) {
	response := contract.TranscriptReadResponse{Steps: []contract.TranscriptStep{}, Turns: []contract.TranscriptTurn{}}
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	if req.Page != "" && req.Page != contract.TranscriptPageOlderItems && req.Page != contract.TranscriptPageMandatoryItems && req.Page != contract.TranscriptPageMandatoryPosts {
		return response, invalid("invalid transcript page")
	}
	if req.Session == "" || req.Limit < 0 || req.Limit > 20 || req.Offset < 0 || req.Cursor < 0 || req.PostIndex < 0 || (req.Page == "" && req.Cursor != 0) || (req.Page != "" && (req.Turn == "" || req.Item != "" || req.AfterItem != "" || req.Tail || req.PostIndex != 0)) || (req.Turn != "" && req.Before != "") || ((req.Item != "" || req.AfterItem != "" || req.Tail || req.PostIndex != 0) && req.Turn == "") {
		return response, invalid("invalid transcript request")
	}
	roomID, err := s.store.TranscriptRoom(ctx, req.Session)
	if errors.Is(err, store.ErrNotFound) {
		return response, &contract.Error{Code: contract.ErrNotFound, Message: "session not found"}
	}
	if err != nil {
		return response, failure(err)
	}
	dir := s.layout.Transcripts(roomID, req.Session)
	files, err := s.store.TranscriptFiles(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	reader := pi.TranscriptReader{Dir: dir, Acked: make(map[string]int64, len(files))}
	for _, f := range files {
		reader.Acked[f.Path] = f.Acked
	}
	deliveries, err := s.store.TranscriptDeliveries(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	// Backfill pre-index files once, under the same lock as appends.
	s.transcriptMu.Lock()
	for _, f := range files {
		if f.Indexed {
			continue
		}
		file, err := os.Open(filepath.Join(dir, f.Path))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			s.transcriptMu.Unlock()
			return response, failure(err)
		}
		err = s.indexTranscript(ctx, req.Session, f.Path, file, 0, f.Acked)
		file.Close()
		if err != nil {
			s.transcriptMu.Unlock()
			return response, failure(err)
		}
	}
	s.transcriptMu.Unlock()
	limit := req.Limit
	if limit == 0 {
		limit = 6
	}
	work, err := s.store.TranscriptTurnMetadata(ctx, req.Session, req.Before, req.Turn, limit+1)
	if errors.Is(err, store.ErrInvalidWorkCursor) {
		return response, invalid("invalid turn cursor")
	}
	if err != nil {
		return response, failure(err)
	}
	if len(work) > limit {
		response.More = true
		work = work[:limit]
	}
	for _, w := range work {
		started := w.Started.Body.(contract.TurnStarted)
		t := contract.TranscriptTurn{ID: started.Turn, Started: w.Started.Time, Inputs: []contract.TranscriptInput{}, Items: []contract.TranscriptItem{}}
		if w.Ended != nil {
			t.Ended = &w.Ended.Time
			t.DurationMS = w.Ended.Time.Sub(w.Started.Time).Milliseconds()
			end := w.Ended.Body.(contract.TurnEnded)
			t.Outcome, t.Cause = end.Outcome, end.Cause
		}
		after := req.AfterItem == ""
		for _, input := range w.Inputs {
			if req.Item == "" && (req.Page != "" || req.Tail) {
				break
			}
			if req.Item != "" && req.Item != "input:"+string(input.ID) {
				continue
			}
			data, err := json.Marshal(input.Body)
			if err != nil {
				return response, failure(err)
			}
			value := string(data)
			var home *contract.Conversation
			var attachments []contract.Attachment
			if body, ok := input.Body.(contract.PostCreated); ok {
				value = body.Text
				home = &body.Home
				attachments = sanitizeTranscriptAttachments(body.Attachments)
			}
			if body, ok := input.Body.(contract.QuestionAnswered); ok {
				if value, err = room.RenderDelivery(body); err != nil {
					return response, failure(err)
				}
				home = &contract.Conversation{Kind: contract.ConversationDM, Session: body.Session}
			}
			value = harness.Mask(value)
			if req.Item != "" {
				return transcriptChunk(response, value, req.Offset)
			}
			if !after {
				if req.AfterItem == "input:"+string(input.ID) {
					after = true
				}
				continue
			}
			if len(t.Inputs) == 8 {
				t.More = true
				break
			}
			t.Inputs = append(t.Inputs, contract.TranscriptInput{Home: home, At: input.Time, ID: input.ID, Seq: input.Seq, Room: input.Room, Type: input.Body.EventType(), From: input.From, Text: clipTranscript(value, 512), More: len(value) > 512, Attachments: attachments})
			// Reserve room for turn metadata and cursor fields before adding items.
			encoded, _ := json.Marshal(t)
			if len(encoded) > 170<<10 && len(t.Inputs) > 1 {
				t.Inputs = t.Inputs[:len(t.Inputs)-1]
				t.More = true
				break
			}
			t.After = "input:" + string(input.ID)
		}
		if !t.More {
			t.After = ""
			ranges, err := s.store.TranscriptTurnRanges(ctx, req.Session, t.ID)
			if err != nil {
				return response, failure(err)
			}
			skip := req.Cursor
			if !req.Tail && req.Page != contract.TranscriptPageMandatoryPosts {
				const maxItems = 256
				var pageItems []pi.TranscriptItem
				seenTotal := 0
				readerMore := false
				for _, r := range ranges {
					var items []pi.TranscriptItem
					var seen int
					var more bool
					var err error
					if req.Item == "" && req.Page == "" {
						items, seen, more, err = reader.ReadRecent(r.Path, r.Start, r.End, deliveries, maxItems)
					} else {
						items, seen, more, err = reader.Read(r.Path, r.Start, r.End, deliveries, req.Item, skip, max(0, maxItems-len(pageItems)))
					}
					if err != nil {
						return response, failure(err)
					}
					for i := range items {
						items[i].Index += seenTotal
					}
					seenTotal += seen
					if skip > seen {
						skip -= seen
					} else {
						skip = 0
					}
					pageItems = append(pageItems, items...)
					if len(pageItems) > maxItems {
						latestText := -1
						for i := range pageItems {
							if pageItems[i].Kind == contract.TranscriptText {
								latestText = i
							}
						}
						drop := len(pageItems) - maxItems
						if latestText >= 0 && latestText < drop {
							pinned := pageItems[latestText]
							pageItems = pageItems[drop:]
							pageItems[0] = pinned
						} else {
							pageItems = pageItems[drop:]
						}
						more = true
					}
					readerMore = readerMore || more
				}
				if readerMore && req.Item == "" {
					if req.Page == contract.TranscriptPageMandatoryItems {
						t.MandatoryItems, t.NextMandatory = true, req.Cursor+len(pageItems)
					} else if req.Page == contract.TranscriptPageOlderItems {
						t.OlderItems, t.NextItem = true, req.Cursor+len(pageItems)
					} else {
						t.MandatoryItems, t.OlderItems = true, true
					}
				}

				callIDs := make([]string, 0, len(pageItems))
				for _, item := range pageItems {
					if item.Kind == contract.TranscriptTool && item.CallID != "" {
						callIDs = append(callIDs, item.CallID)
					}
				}
				itemsByCall := make(map[string]*pi.TranscriptItem, len(pageItems))
				for i := range pageItems {
					if pageItems[i].CallID != "" {
						itemsByCall[pageItems[i].CallID] = &pageItems[i]
					}
				}
				err := s.store.TranscriptCallPosts(ctx, req.Session, t.ID, callIDs, maxTranscriptCallPosts+1, func(callID string, event contract.Event, total int) {
					item := itemsByCall[callID]
					added := false
					if len(item.Posts) < maxTranscriptCallPosts && item.PostsNext == nil {
						post := contract.TranscriptItem{ID: string(event.ID), Turn: t.ID, At: event.Time}
						applyTranscriptPost(&post, event)
						item.Posts = append(item.Posts, post)
						encoded, _ := json.Marshal(item.TranscriptItem)
						if len(encoded) > 80<<10 {
							item.Posts = item.Posts[:len(item.Posts)-1]
						} else {
							added = true
						}
					}
					item.PostsOmitted = total - len(item.Posts)
					if item.PostsOmitted > 0 && item.PostsNext == nil && !added {
						item.PostsNext = &contract.TranscriptPostLocation{Room: event.Room, Seq: event.Seq}
					}
				})
				if err != nil {
					return response, failure(err)
				}
				for _, item := range pageItems {
					t.Items = append(t.Items, item.TranscriptItem)
				}

			}
			if req.Item != "" {
				if len(t.Items) == 0 {
					event, found, err := s.store.TranscriptTurnPost(ctx, req.Session, t.ID, contract.EventID(req.Item))
					if err != nil {
						return response, failure(err)
					}
					if !found {
						return response, invalid("unknown transcript item")
					}
					return transcriptChunk(response, harness.Mask(event.Body.(contract.PostCreated).Text), req.Offset)
				}
				item := t.Items[0]
				if item.Kind == contract.TranscriptPost && req.Part == contract.TranscriptPartText {
					event, found, err := s.store.TranscriptTurnPost(ctx, req.Session, t.ID, item.Event)
					if err != nil {
						return response, failure(err)
					}
					if found {
						return transcriptChunk(response, harness.Mask(event.Body.(contract.PostCreated).Text), req.Offset)
					}
				}
				value := item.Output
				switch req.Part {
				case "", contract.TranscriptPartOutput:
				case contract.TranscriptPartText:
					value = item.Text
				case contract.TranscriptPartArguments:
					value = item.Arguments
				case contract.TranscriptPartScript:
					value = item.Script
				case contract.TranscriptPartDiff:
					value = item.Diff
				default:
					return response, invalid("invalid transcript item part")
				}
				return transcriptChunk(response, value, req.Offset)
			}
			// Scan a bounded newest-first page, separately from Pi items.
			var posts []contract.Event
			const postPageSize = 8
			if req.Page != contract.TranscriptPageMandatoryItems && req.Page != contract.TranscriptPageOlderItems {
				posts, err = s.store.TranscriptSentPostsNewest(ctx, req.Session, t.ID, postPageSize, req.Cursor)
				if err != nil {
					return response, failure(err)
				}
				for i := len(posts) - 1; i >= 0; i-- {
					post := posts[i]
					item := contract.TranscriptItem{ID: string(post.ID), Index: 1<<30 + int(post.Seq), Turn: t.ID, At: post.Time}
					applyTranscriptPost(&item, post)
					t.Items = append(t.Items, item)
				}
			}

			budgetTranscriptTurn(&t, req, 180<<10)
			if len(posts) > 0 {
				kept := make(map[string]bool, len(t.Items))
				nested := make(map[string]bool)
				for _, item := range t.Items {
					for _, post := range item.Posts {
						nested[post.ID] = true
					}
				}
				filtered := t.Items[:0]
				for _, item := range t.Items {
					kept[item.ID] = true
					if item.Kind != contract.TranscriptPost || !nested[item.ID] {
						filtered = append(filtered, item)
					}
				}
				t.Items = filtered
				for id := range nested {
					kept[id] = true
				}
				for _, post := range posts {
					if !kept[string(post.ID)] {
						t.MandatoryPosts, t.NextMandatoryPost = true, int(post.Seq)
						break
					}
				}
				if !t.MandatoryPosts && len(posts) == postPageSize && posts[len(posts)-1].Seq > 1 {
					t.MandatoryPosts, t.NextMandatoryPost = true, int(posts[len(posts)-1].Seq-1)
				}
				t.More = t.More || t.MandatoryPosts
			}

		}
		if req.Turn != "" {
			for _, item := range t.Items {
				if item.Kind == contract.TranscriptTool || item.Kind == contract.TranscriptPost {
					response.Steps = append(response.Steps, pi.TranscriptStep(item))
				}
			}
			response.Truncated = t.More
		}
		response.Turns = append(response.Turns, t)
		data, _ := json.Marshal(response)
		if len(data) > 256<<10 && len(response.Turns) > 1 {
			response.Turns = response.Turns[:len(response.Turns)-1]
			response.More = true
			break
		}
	}
	if req.Item != "" {
		return response, invalid("unknown transcript item")
	}
	if response.More && len(response.Turns) > 0 {
		response.Before = response.Turns[len(response.Turns)-1].ID
	}
	return response, nil
}

func transcriptChunk(response contract.TranscriptReadResponse, value string, offset int) (contract.TranscriptReadResponse, *contract.Error) {
	if offset > len(value) || !utf8.ValidString(value[:offset]) {
		return response, invalid("invalid transcript offset")
	}
	end := min(len(value), offset+(64<<10))
	for !utf8.ValidString(value[offset:end]) {
		end--
	}
	response.Chunk = value[offset:end]
	if end < len(value) {
		response.NextOffset = end
	}
	return response, nil
}

func clipTranscript(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return strings.ToValidUTF8(value[:maxBytes], "")
}

func applyTranscriptPost(item *contract.TranscriptItem, event contract.Event) {
	post := event.Body.(contract.PostCreated)
	text := harness.Mask(post.Text)
	item.Kind, item.Event, item.Room, item.Seq, item.Text = contract.TranscriptPost, event.ID, event.Room, event.Seq, clipTranscript(text, 4096)
	from := event.From
	item.From = &from
	item.More = item.More || len(text) > 4096
	home := post.Home
	item.Home = &home
	item.Attachments = sanitizeTranscriptAttachments(post.Attachments)
}

func sanitizeTranscriptAttachments(attachments []contract.Attachment) []contract.Attachment {
	attachments = slices.Clone(attachments)
	for i := range attachments {
		a := &attachments[i]
		a.Name = clipTranscript(harness.Mask(a.Name), 128)
		a.Media = clipTranscript(harness.Mask(a.Media), 128)
		if a.Visual != nil {
			visual := *a.Visual
			visual.Checks = slices.Clone(visual.Checks)
			a.Visual = &visual
			a.Visual.Title = clipTranscript(harness.Mask(a.Visual.Title), 128)
			for j := range a.Visual.Checks {
				a.Visual.Checks[j].Detail = clipTranscript(harness.Mask(a.Visual.Checks[j].Detail), 128)
			}
		}
	}
	return attachments
}

// budgetTranscriptTurn retains the final text, then the newest mandatory items,
// then tools. Mandatory continuations advance forward and contain no tools.
func budgetTranscriptTurn(t *contract.TranscriptTurn, req contract.TranscriptReadRequest, budget int) {
	items := t.Items
	t.Items = nil
	base, _ := json.Marshal(t)
	size := len(base) + 512 // Reserve cursor and JSON framing bytes.
	stepSize := 0
	keep := make([]bool, len(items))
	mandatory := func(item contract.TranscriptItem) bool {
		return item.Kind != contract.TranscriptTool || len(item.Posts) > 0 || item.PostsOmitted > 0
	}
	add := func(i int) bool {
		data, _ := json.Marshal(items[i])
		stepBytes := 0
		if req.Turn != "" && (items[i].Kind == contract.TranscriptTool || items[i].Kind == contract.TranscriptPost) {
			step, _ := json.Marshal(pi.TranscriptStep(items[i]))
			stepBytes = len(step) + 1
		}
		if size+len(data)+1 > budget || size+len(data)+1+stepSize+stepBytes > 250<<10 {
			return false
		}
		keep[i] = true
		size += len(data) + 1
		stepSize += stepBytes
		return true
	}
	if req.Page == contract.TranscriptPageMandatoryItems {
		for i, item := range items {
			if item.Index < req.Cursor || !mandatory(item) {
				continue
			}
			if !add(i) {
				if !t.MandatoryItems || item.Index < t.NextMandatory {
					t.NextMandatory = item.Index
				}
				t.MandatoryItems = true
				break
			}
		}
	} else if req.Page == contract.TranscriptPageMandatoryPosts {
		for i := len(items) - 1; i >= 0; i-- {
			add(i)
		}
	} else if req.Page == contract.TranscriptPageOlderItems {
		for i, item := range items {
			if !add(i) {
				if !t.OlderItems || item.Index < t.NextItem {
					t.NextItem = item.Index
				}
				t.OlderItems = true
				break
			}
		}
	} else {
		// Reserve the final text before selecting newer posts.
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].Kind == contract.TranscriptText {
				add(i)
				break
			}
		}
		for i := len(items) - 1; i >= 0; i-- {
			if !mandatory(items[i]) || keep[i] {
				continue
			}
			if !add(i) {
				if items[i].Kind != contract.TranscriptPost {
					if !t.MandatoryItems || items[i].Index < t.NextMandatory {
						t.NextMandatory = items[i].Index
					}
					t.MandatoryItems = true
				}
			}
		}
		for i := len(items) - 1; i >= 0; i-- {
			if mandatory(items[i]) {
				continue
			}
			if !add(i) {
				if !t.OlderItems || items[i].Index < t.NextItem {
					t.NextItem = items[i].Index
				}
				t.OlderItems = true
			}
		}
	}
	for i, item := range items {
		if keep[i] {
			t.Items = append(t.Items, item)
		}
	}
	if t.Items == nil {
		t.Items = []contract.TranscriptItem{}
	}
	t.More = t.More || t.OlderItems || t.MandatoryItems || t.MandatoryPosts
}
