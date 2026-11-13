package engine

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/resident-labs/dyna/engine/transcriptview"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pig"
)

const maxTranscriptCallPosts = 8

// A turn's inputs come at most 50 and 48 KiB of encoded inputs to a page, whichever is reached first.
const (
	maxTranscriptInputs     = 50
	maxTranscriptInputBytes = 48 << 10
)

// TranscriptRead reads the acknowledged prefix without holding the writer or service lock.
func (s *Service) TranscriptRead(ctx context.Context, caller contract.Principal, req contract.TranscriptReadRequest) (contract.TranscriptReadResponse, *contract.Error) {
	response := contract.TranscriptReadResponse{Steps: []contract.TranscriptStep{}, Turns: []contract.TranscriptTurn{}}
	if req.Page != "" && req.Page != contract.TranscriptPageOlderItems && req.Page != contract.TranscriptPageMandatoryItems && req.Page != contract.TranscriptPageMandatoryPosts {
		return response, invalid("invalid transcript page")
	}
	if req.Session == "" || req.Limit < 0 || req.Limit > 20 || req.Offset < 0 || req.Cursor < 0 || req.PostIndex < 0 || (req.Page == "" && req.Cursor != 0) || (req.Page != "" && (req.Turn == "" || req.Item != "" || req.AfterItem != "" || req.Tail || req.PostIndex != 0)) || (req.Turn != "" && req.Before != "") || ((req.Item != "" || req.AfterItem != "" || req.Tail || req.PostIndex != 0) && req.Turn == "") {
		return response, invalid("invalid transcript request")
	}
	var worker store.WorkerTranscript
	if req.Step != "" {
		var err error
		if worker, err = s.store.WorkflowWorker(ctx, req.Session, req.Step); err != nil {
			return response, failure(err)
		}
		if worker.Turn == "" {
			return response, nil // It has not started, or nothing of it has reached the engine.
		}
		req.Session = worker.Session
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
	reader := pig.TranscriptReader{Dir: dir, Acked: make(map[string]int64, len(files))}
	for _, f := range files {
		reader.Acked[f.Path] = f.Acked
	}
	var deliveries map[contract.DeliverySeq]contract.TurnID
	mapsLoaded := false
	limit := req.Limit
	if limit == 0 {
		limit = 6
	}
	exact := req.Turn
	if req.Step != "" {
		exact = worker.Turn
	}
	work, err := s.store.TranscriptTurnMetadata(ctx, req.Session, req.Before, exact, limit+1)
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
		if req.Step != "" {
			w.Inputs = nil
			transcriptview.ApplyWorkerTurn(&t, worker, w.Started)
		}
		after := req.AfterItem == ""
		inputBytes := 0
		for _, input := range w.Inputs {
			if req.Item == "" && (req.Page != "" || req.Tail) {
				break
			}
			if req.Item != "" && req.Item != "input:"+string(input.ID) {
				continue
			}
			value, home, attachments, postData, dataAttachment, err := transcriptview.InputBody(input)
			if err != nil {
				return response, failure(err)
			}
			if req.Item != "" {
				return transcriptChunk(response, value, req.Offset)
			}
			if !after {
				if req.AfterItem == "input:"+string(input.ID) {
					after = true
				}
				continue
			}
			if len(t.Inputs) == maxTranscriptInputs {
				t.More = true
				break
			}
			var source *contract.PostSource
			if post, ok := input.Body.(contract.PostCreated); ok {
				source = post.Source
			}
			read := contract.TranscriptInput{Data: postData, DataAttachment: dataAttachment, Source: source, Home: home, At: input.Time, ID: input.ID, Seq: input.Seq, Room: input.Room, Type: input.Body.EventType(), From: input.From, Text: transcriptview.Clip(value, 512), More: len(value) > 512, Attachments: attachments}
			read.Data, read.DataRef = transcriptview.Data(read.Data, read.Attachments, read.DataAttachment)
			encoded, _ := json.Marshal(read)
			if inputBytes+len(encoded)+1 > maxTranscriptInputBytes && len(t.Inputs) > 0 {
				t.More = true
				break
			}
			inputBytes += len(encoded) + 1
			t.Inputs = append(t.Inputs, read)
			t.After = "input:" + string(input.ID)
		}
		// The turn's first page carries its items with the inputs. A page that continues the inputs carries only inputs.
		if !t.More {
			t.After = ""
		}
		if req.AfterItem == "" || req.Item != "" {
			ranges, err := s.store.TranscriptTurnRanges(ctx, req.Session, t.ID)
			if err != nil {
				return response, failure(err)
			}
			if req.Step != "" {
				ranges = slices.DeleteFunc(ranges, func(r store.TranscriptTurnRange) bool { return r.Path != worker.Step.Transcript })
			}
			// Every request reads the window of items, because a post's index is where it falls among them.
			wantItems := !req.Tail && req.Page != contract.TranscriptPageMandatoryPosts
			skip := req.Cursor
			const maxItems = 256
			var pageItems []pig.TranscriptItem
			seenTotal := 0
			readerMore := false
			for _, r := range ranges {
				// Load attribution only when a Pi range is actually read, once per request.
				if !mapsLoaded {
					deliveries, err = s.store.TranscriptDeliveries(ctx, req.Session)
					if err != nil {
						return response, failure(err)
					}
					reader.Steers, err = s.store.TranscriptSteers(ctx, req.Session)
					if err != nil {
						return response, failure(err)
					}
					mapsLoaded = true
				}
				var items []pig.TranscriptItem
				var seen int
				var more bool
				var err error
				if req.Item == "" && (req.Page == "" || !wantItems) {
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
			if wantItems {
				if readerMore && req.Item == "" {
					if req.Page == contract.TranscriptPageMandatoryItems {
						t.MandatoryItems, t.NextMandatory = true, req.Cursor+len(pageItems)
					} else if req.Page == contract.TranscriptPageOlderItems {
						t.OlderItems, t.NextItem = true, req.Cursor+len(pageItems)
					} else {
						t.MandatoryItems, t.OlderItems = true, true
					}
				}

				for i := range pageItems {
					if pageItems[i].Delivery == 0 {
						continue
					}
					event, err := s.store.Event(ctx, int64(pageItems[i].Delivery))
					if err != nil {
						return response, failure(err)
					}
					if err := transcriptview.ApplySteer(&pageItems[i], event, req.Item != ""); err != nil {
						return response, failure(err)
					}
				}

				callIDs := make([]string, 0, len(pageItems))
				for _, item := range pageItems {
					if item.Kind == contract.TranscriptTool && item.CallID != "" {
						callIDs = append(callIDs, item.CallID)
					}
				}
				itemsByCall := make(map[string]*pig.TranscriptItem, len(pageItems))
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
						transcriptview.ApplyPost(&post, event)
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
			// The crew's posts have no worker to belong to.
			if req.Step == "" && req.Page != contract.TranscriptPageMandatoryItems && req.Page != contract.TranscriptPageOlderItems {
				posts, err = s.store.TranscriptSentPostsNewest(ctx, req.Session, t.ID, postPageSize, req.Cursor)
				if err != nil {
					return response, failure(err)
				}
				for i := len(posts) - 1; i >= 0; i-- {
					post := posts[i]
					// The post's place among the items: before the first one that began after it was sent, or after all of them.
					index := seenTotal
					for _, item := range pageItems {
						if post.Time.Before(item.At) {
							index = item.Index
							break
						}
					}
					item := contract.TranscriptItem{ID: string(post.ID), Index: index, Turn: t.ID, At: post.Time}
					transcriptview.ApplyPost(&item, post)
					t.Items = append(t.Items, item)
				}
			}

			transcriptview.BudgetTurn(&t, req, 180<<10)
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
					response.Steps = append(response.Steps, pig.TranscriptStep(item))
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
