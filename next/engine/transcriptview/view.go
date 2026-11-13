package transcriptview

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
	"github.com/resident-labs/dyna/harness"
	"github.com/resident-labs/dyna/harness/pig"
	"github.com/resident-labs/dyna/room"
)

// ApplyWorkerTurn makes a crew's turn read as one worker: its times, and its prompt as what it read. The crew's turn is the
// script's, so its outcome says nothing about this worker. The run page shows the step's own.
func ApplyWorkerTurn(t *contract.TranscriptTurn, worker store.WorkerTranscript, started contract.Event) {
	step := worker.Step
	t.Started, t.Ended, t.DurationMS, t.Outcome, t.Cause = step.Started, nil, 0, "", ""
	if !step.Ended.IsZero() {
		t.Ended, t.DurationMS = &step.Ended, step.Ended.Sub(step.Started).Milliseconds()
	}
	t.Inputs = []contract.TranscriptInput{{ID: contract.EventID("step:" + step.Key), At: step.Started, Room: started.Room, Type: contract.PostCreated{}.EventType(), From: contract.Principal{Kind: contract.PrincipalSession, Session: worker.Session}, Text: harness.Mask(worker.Prompt)}}
}

func Clip(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return strings.ToValidUTF8(value[:maxBytes], "")
}

func ApplyPost(item *contract.TranscriptItem, event contract.Event) {
	post := event.Body.(contract.PostCreated)
	text := harness.Mask(post.Text)
	item.Kind, item.Event, item.Room, item.Seq, item.Text = contract.TranscriptPost, event.ID, event.Room, event.Seq, Clip(text, 4096)
	from := event.From
	item.From = &from
	item.More = item.More || len(text) > 4096
	home := post.Home
	item.Home = &home
	for _, to := range post.Mentions {
		if to.Kind == contract.PrincipalSession {
			item.Mentions = append(item.Mentions, to.Session)
		}
	}
	item.DataAttachment = post.DataAttachment
	item.Data, item.DataRef = Data(post.Data, post.Attachments, post.DataAttachment)
	item.Attachments = SanitizeAttachments(post.Attachments)
}

// InputBody returns the masked text of an event a session read, with where it was sent and what it carried.
func InputBody(input contract.Event) (string, *contract.Conversation, []contract.Attachment, contract.JSONData, *int, error) {
	body := input.Body
	var postData contract.JSONData
	var dataAttachment *int
	var finalFiles []contract.Attachment
	if ended, ok := body.(contract.TurnEnded); ok {
		postData, finalFiles, dataAttachment = ended.Data, ended.Attachments, ended.DataAttachment
		ended.Data, ended.Attachments, ended.DataAttachment = "", nil, nil
		body = ended
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", nil, nil, "", nil, err
	}
	value := string(data)
	var home *contract.Conversation
	attachments := SanitizeAttachments(finalFiles)
	if post, ok := input.Body.(contract.PostCreated); ok {
		value = post.Text
		postData, dataAttachment = post.Data, post.DataAttachment
		home = &post.Home
		attachments = SanitizeAttachments(post.Attachments)
	}
	if answer, ok := input.Body.(contract.QuestionAnswered); ok {
		if value, err = room.RenderDelivery(answer); err != nil {
			return "", nil, nil, "", nil, err
		}
		home = &contract.Conversation{Kind: contract.ConversationDM, Session: answer.Session}
	}
	return harness.Mask(value), home, attachments, postData, dataAttachment, nil
}

// ApplySteer fills a steer from its event. Pi's record holds the frame the session read, not what the owner wrote.
func ApplySteer(item *pig.TranscriptItem, event contract.Event, full bool) error {
	value, home, attachments, postData, dataAttachment, err := InputBody(event)
	if err != nil {
		return err
	}
	from := event.From
	item.Event, item.Type, item.Room, item.Seq, item.At, item.From, item.Home, item.Attachments = event.ID, event.Body.EventType(), event.Room, event.Seq, event.Time, &from, home, attachments
	item.DataAttachment = dataAttachment
	item.Data, item.DataRef = Data(postData, attachments, dataAttachment)
	item.Text, item.More = value, false
	if !full {
		item.Text, item.More = Clip(value, 4096), len(value) > 4096
	}
	return nil
}

func SanitizeAttachments(attachments []contract.Attachment) []contract.Attachment {
	attachments = slices.Clone(attachments)
	for i := range attachments {
		a := &attachments[i]
		a.Name = Clip(harness.Mask(a.Name), 128)
		a.Media = Clip(harness.Mask(a.Media), 128)
		if a.Visual != nil {
			visual := *a.Visual
			visual.Checks = slices.Clone(visual.Checks)
			a.Visual = &visual
			a.Visual.Title = Clip(harness.Mask(a.Visual.Title), 128)
			for j := range a.Visual.Checks {
				a.Visual.Checks[j].Detail = Clip(harness.Mask(a.Visual.Checks[j].Detail), 128)
			}
		}
	}
	return attachments
}

// BudgetTurn retains the final text, then the newest mandatory items,
// then tools. Mandatory continuations advance forward and contain no tools.
func BudgetTurn(t *contract.TranscriptTurn, req contract.TranscriptReadRequest, budget int) {
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
			step, _ := json.Marshal(pig.TranscriptStep(items[i]))
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

// Data uses the constructor's associated file index, not a filename or content-hash guess.
func Data(data contract.JSONData, files []contract.Attachment, associated *int) (contract.JSONData, *contract.PostDataRef) {
	if len(data) <= 32<<10 || associated == nil {
		return data, nil
	}
	file := files[*associated]
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	token, _ := decoder.Token() // Canonical JSON is already validated.
	summary := "null"
	switch value := token.(type) {
	case json.Delim:
		count := 0
		for decoder.More() {
			if value == '{' {
				_, _ = decoder.Token()
			}
			var child json.RawMessage
			_ = decoder.Decode(&child) // Skip a child without recursively materializing a Go value.
			count++
		}
		noun := "items"
		if value == '{' {
			noun = "keys"
		}
		summary = fmt.Sprintf("%d %s", count, noun)
	case string:
		summary = "string"
	case bool:
		summary = "boolean"
	case json.Number:
		summary = "number"
	}
	return "", &contract.PostDataRef{File: file, Summary: summary}
}
