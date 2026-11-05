package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
)

const postDataInlineBytes = 1024

var errPostDataUTF8 = errors.New("post data is not valid UTF-8")

// preparePostData owns validation/encoding and persists large values before delivery references them.
// JSONData is encoded text on the wire; its inner JSON is validated here, without decoding numbers.
func (s *Service) preparePostData(ctx context.Context, room contract.RoomID, data contract.JSONData, files []contract.Attachment) (contract.JSONData, []contract.Attachment, *int, error) {
	if len(data) == 0 {
		return "", files, nil, nil
	}
	if !utf8.ValidString(string(data)) {
		return "", nil, nil, errPostDataUTF8
	}
	var encoded bytes.Buffer
	if err := json.Compact(&encoded, []byte(data)); err != nil {
		return "", nil, nil, fmt.Errorf("invalid post data: %w", err)
	}
	canonical, _ := json.Marshal(json.RawMessage(encoded.Bytes())) // HTML-safe, retaining number and string token spellings.
	data = contract.JSONData(canonical)
	if len(data) <= postDataInlineBytes {
		return data, files, nil, nil
	}
	hash, n, err := s.saveFile(ctx, room, strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		return "", nil, nil, err
	}
	// Exact matching content is validated JSON, even if an existing copy was uploaded as plain text.
	files = slices.Clone(files)
	for i := range files {
		if files[i].SHA256 == hash {
			files[i].Media = "application/json"
		}
	}
	index := slices.IndexFunc(files, func(file contract.Attachment) bool {
		return file.SHA256 == hash && file.Name == "data.json" && file.Media == "application/json"
	})
	if index < 0 {
		index = len(files)
		files = append(files, contract.Attachment{SHA256: hash, Name: "data.json", Media: "application/json", Bytes: n})
	}
	return data, files, &index, nil
}

// deliveryData adds only explicitly encoded data; post text is not inspected.
func (s *Service) deliveryData(event contract.Event, text string) string {
	var data contract.JSONData
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
		return text + "\n\ndata: attachment unavailable"
	}
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, []byte(data), "", "  ") // Validated by the owning boundary.
	return text + "\n\ndata:\n" + pretty.String()
}

// transcriptData uses the constructor's associated file index, not a filename or content-hash guess.
func transcriptData(data contract.JSONData, files []contract.Attachment, associated *int) (contract.JSONData, *contract.PostDataRef) {
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
