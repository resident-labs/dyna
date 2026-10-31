package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pi"
)

// TranscriptRead reads the acknowledged prefix without holding the writer or service lock.
func (s *Service) TranscriptRead(ctx context.Context, caller contract.Principal, req contract.TranscriptReadRequest) (contract.TranscriptReadResponse, *contract.Error) {
	response := contract.TranscriptReadResponse{Steps: []contract.TranscriptStep{}, Turns: []contract.TranscriptTurn{}}
	if caller.Kind != contract.PrincipalOwner {
		return response, forbidden()
	}
	if req.Session == "" || req.Limit < 0 || req.Limit > 20 || req.Offset < 0 || req.ItemIndex < 0 || req.PostIndex < 0 || (req.Turn != "" && req.Before != "") || ((req.Item != "" || req.ItemIndex != 0 || req.PostIndex != 0 || req.Tail || req.AfterItem != "") && req.Turn == "") {
		return response, invalid("invalid transcript request")
	}
	exists, err := s.store.TranscriptSessionExists(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	if !exists {
		return response, &contract.Error{Code: contract.ErrNotFound, Message: "session not found"}
	}
	files, err := s.store.TranscriptFiles(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	deliveries, err := s.store.TranscriptDeliveries(ctx, req.Session)
	if err != nil {
		return response, failure(err)
	}
	// Backfill pre-index files once, under the same lock as appends.
	s.transcriptMu.Lock()
	for _, f := range files {
		indexed, err := s.store.TranscriptPathIndexed(ctx, req.Session, f.Path)
		if err != nil {
			s.transcriptMu.Unlock()
			return response, failure(err)
		}
		if indexed {
			continue
		}
		file, err := os.Open(filepath.Join(s.transcriptDir, string(req.Session), f.Path))
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
			if req.Item == "" && (req.ItemIndex > 0 || req.Tail) {
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
			if body, ok := input.Body.(contract.PostCreated); ok {
				value = body.Text
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
			t.Inputs = append(t.Inputs, contract.TranscriptInput{ID: input.ID, Seq: input.Seq, Room: input.Room, Type: input.Body.EventType(), From: input.From, Text: clipTranscript(value, 4096), More: len(value) > 4096})
			t.After = "input:" + string(input.ID)
		}
		if !t.More {
			t.After = ""
			ranges, err := s.store.TranscriptTurnRanges(ctx, req.Session, t.ID)
			if err != nil {
				return response, failure(err)
			}
			skip := req.ItemIndex
			if !req.Tail {
				for _, r := range ranges {
					file, err := os.Open(filepath.Join(s.transcriptDir, string(req.Session), r.Path))
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					if err != nil {
						return response, failure(err)
					}
					info, err := file.Stat()
					if err != nil {
						file.Close()
						return response, failure(err)
					}
					end := info.Size()
					if r.End != nil && *r.End < end {
						end = *r.End
					}
					// Never read bytes after the committed acknowledgment.
					for _, f := range files {
						if f.Path == r.Path && f.Acked < end {
							end = f.Acked
							break
						}
					}
					items, seen, more, err := pi.ReadTranscript(io.NewSectionReader(file, r.Start, max(0, end-r.Start)), deliveries, r.Path, req.Item, skip, max(0, 6-len(t.Items)))
					file.Close()
					if err != nil {
						return response, failure(err)
					}
					for i := range items {
						item := &items[i]
						if item.Kind != contract.TranscriptTool || item.Tool != "bash" {
							continue
						}
						id := contract.EventID(strings.TrimSpace(item.Output))
						event, found, err := s.store.TranscriptTurnPost(ctx, req.Session, t.ID, id)
						if err != nil {
							return response, failure(err)
						}
						if !found {
							continue
						}
						applyTranscriptPost(item, event)

					}
					if skip > seen {
						skip -= seen
					} else {
						skip = 0
					}
					t.Items = append(t.Items, items...)
					t.More = t.More || more
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
				default:
					return response, invalid("invalid transcript item part")
				}
				return transcriptChunk(response, value, req.Offset)
			}
			for len(t.Items) > 0 {
				data, _ := json.Marshal(t)
				if len(data) <= 180<<10 {
					break
				}
				t.Items = t.Items[:len(t.Items)-1]
				t.More = true
			}
			if t.More {
				t.NextItem = req.ItemIndex + len(t.Items)
			} else {
				// Posts with no matching Pi tool result remain visible in a bounded tail.
				offset := req.PostIndex
				for len(t.Items) < 6 {
					posts, err := s.store.TranscriptSentPosts(ctx, req.Session, t.ID, 1, offset)
					if err != nil {
						return response, failure(err)
					}
					if len(posts) == 0 {
						break
					}
					post := posts[0]
					offset++
					claimed := false
					for _, r := range ranges {
						file, err := os.Open(filepath.Join(s.transcriptDir, string(req.Session), r.Path))
						if errors.Is(err, os.ErrNotExist) {
							continue
						}
						if err != nil {
							return response, failure(err)
						}
						end := int64(0)
						for _, f := range files {
							if f.Path == r.Path {
								end = f.Acked
								break
							}
						}
						if r.End != nil && *r.End < end {
							end = *r.End
						}
						found, err := pi.TranscriptHasPost(io.NewSectionReader(file, r.Start, max(0, end-r.Start)), post.ID)
						file.Close()
						if err != nil {
							return response, failure(err)
						}
						if found {
							claimed = true
							break
						}
					}
					if claimed {
						continue
					}
					item := contract.TranscriptItem{ID: string(post.ID), Turn: t.ID, At: post.Time}
					applyTranscriptPost(&item, post)
					t.Items = append(t.Items, item)
				}
				if len(t.Items) == 6 {
					t.More = true
					t.Tail = true
					t.NextPost = offset
				}
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
	item.Kind, item.Event, item.Room, item.Seq, item.Text = contract.TranscriptPost, event.ID, event.Room, event.Seq, clipTranscript(text, 8192)
	item.More = item.More || len(text) > 8192
	home := post.Home
	item.Home = &home
	item.Attachments = post.Attachments
	for i := range item.Attachments {
		a := &item.Attachments[i]
		a.Name = harness.Mask(a.Name)
		if a.Visual != nil {
			a.Visual.Title = harness.Mask(a.Visual.Title)
			for j := range a.Visual.Checks {
				a.Visual.Checks[j].Detail = harness.Mask(a.Visual.Checks[j].Detail)
			}
		}
	}
}
