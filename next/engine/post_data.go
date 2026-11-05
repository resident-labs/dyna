package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/resident-labs/dyna/contract"
)

const postDataInlineBytes = 1024

// preparePostData owns JSON validation/encoding and persists large values before any delivery can reference them.
// Compact preserves number spellings and UTF-8, removes insignificant whitespace, and adds no newline.
func (s *Service) preparePostData(ctx context.Context, room contract.RoomID, data json.RawMessage, files []contract.Attachment) (json.RawMessage, []contract.Attachment, *int, error) {
	if len(data) == 0 {
		return nil, files, nil, nil
	}
	var encoded bytes.Buffer
	if err := json.Compact(&encoded, data); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid post data: %w", err)
	}
	data, _ = json.Marshal(json.RawMessage(encoded.Bytes())) // Match the stored event encoding (HTML-sensitive characters are escaped).
	if len(data) <= postDataInlineBytes {
		return data, files, nil, nil
	}
	hash, n, err := s.saveFile(ctx, room, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, nil, err
	}
	// This exact content is validated JSON, even if an existing copy was uploaded as plain text.
	// Normalize matching media before event_files records the hash's first media type.
	files = slices.Clone(files)
	for i := range files {
		if files[i].SHA256 == hash {
			files[i].Media = "application/json"
		}
	}
	// A forwarded post already carries its data blob. Do not add a second copy.
	index := slices.IndexFunc(files, func(file contract.Attachment) bool {
		return file.SHA256 == hash && file.Name == "data.json" && file.Media == "application/json"
	})
	if index < 0 {
		index = len(files)
		files = append(files, contract.Attachment{SHA256: hash, Name: "data.json", Media: "application/json", Bytes: n})
	}
	return data, files, &index, nil
}

// deliveryData adds only typed data; JSON-looking text is never inspected.
func (s *Service) deliveryData(event contract.Event, text string) string {
	var data json.RawMessage
	var attachments []contract.Attachment
	var index *int
	switch body := event.Body.(type) {
	case contract.PostCreated:
		data, attachments, index = body.Data, body.Attachments, body.DataAttachment
	case contract.TurnEnded:
		data, attachments, index = body.Data, body.Attachments, body.DataAttachment
	}
	if len(data) == 0 {
		return text
	}
	if len(data) > postDataInlineBytes {
		paths := s.layout.BoxAttachments(event.Seq, attachments)
		if index != nil {
			return text + fmt.Sprintf("\n\ndata: %s (%s)", paths[*index], fileSize(attachments[*index].Bytes))
		}
		// Persisted data always has its blob. Never turn a corrupt/old event into an unbounded input.
		return text + "\n\ndata: attachment unavailable"
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, data, "", "  ") // The owning boundary already validated this value.
	return text + "\n\ndata:\n" + pretty.String()
}

// transcriptData bounds the JSON carried inside a transcript page. The complete blob remains on the existing
// authenticated file route; a typed reference lets the web fetch it on expansion rather than lose an oversized post.
func transcriptData(data json.RawMessage, files []contract.Attachment) (json.RawMessage, *contract.PostDataRef) {
	if len(data) <= 32<<10 {
		return data, nil
	}
	if i := dataAttachment(data, files); i >= 0 {
		file := files[i]
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		_ = decoder.Decode(&value) // Already validated and encoded by the post boundary.
		summary := "null"
		switch v := value.(type) {
		case map[string]any:
			summary = fmt.Sprintf("%d keys", len(v))
		case []any:
			summary = fmt.Sprintf("%d items", len(v))
		case string:
			summary = "string"
		case bool:
			summary = "boolean"
		case json.Number:
			summary = "number"
		}
		return nil, &contract.PostDataRef{File: file, Summary: summary}
	}
	return data, nil
}

// Names alone do not identify generated data: a post may already have an unrelated data.json attachment.
func dataAttachment(data json.RawMessage, files []contract.Attachment) int {
	hash := sha256.Sum256(data)
	sum := hex.EncodeToString(hash[:])
	return slices.IndexFunc(files, func(file contract.Attachment) bool { return file.SHA256 == sum && file.Media == "application/json" })
}
