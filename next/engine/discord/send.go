package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"strings"
	"unicode/utf16"
)

// Split respects Discord's UTF-16 limit and preserves fenced blocks across messages.
func Split(text string) []string {
	var chunks []string
	fence := ""
	prefix := ""
	for text != "" {
		capacity := 2000 - len(utf16.Encode([]rune(prefix))) - 8
		runes := []rune(text)
		units, end, lastLine := 0, 0, 0
		for end < len(runes) {
			n := 1
			if runes[end] > 0xffff {
				n = 2
			}
			if units+n > capacity {
				break
			}
			units += n
			end++
			if runes[end-1] == '\n' {
				lastLine = end
			}
		}
		if end < len(runes) && lastLine > 0 {
			end = lastLine
		}
		piece := string(runes[:end])
		text = string(runes[end:])
		for _, line := range strings.Split(piece, "\n") {
			if strings.HasPrefix(line, "```") {
				if fence != "" {
					fence = ""
				} else {
					fence = strings.TrimSpace(strings.TrimPrefix(line, "```"))
					if len([]rune(fence)) > 32 {
						fence = " "
					}
					if fence == "" {
						fence = " "
					}
				}
			}
		}
		chunk := prefix + piece
		prefix = ""
		if fence != "" && text != "" {
			chunk += "\n```"
			prefix = "```" + strings.TrimSpace(fence) + "\n"
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func (c *Client) Send(ctx context.Context, channel, text, reply string) ([]string, int, error) {
	var ids []string
	for _, part := range Split(text) {
		body := map[string]any{"content": part, "allowed_mentions": map[string]any{"parse": []string{}}}
		if reply != "" {
			body["message_reference"] = map[string]any{"message_id": reply, "fail_if_not_exists": false}
		}
		data, _ := json.Marshal(body)
		r, err := c.API(ctx, "POST", fmt.Sprintf("/api/v10/channels/%s/messages", channel), data, "")
		if err != nil {
			return ids, 0, err
		}
		if r.Status < 200 || r.Status >= 300 {
			return ids, r.Status, fmt.Errorf("Discord send failed (HTTP %d)", r.Status)
		}
		var message struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(r.Body, &message) != nil || message.ID == "" {
			return ids, r.Status, errors.New("invalid Discord message response")
		}
		ids = append(ids, message.ID)
	}
	return ids, 200, nil
}

type File struct {
	Name string
	Data []byte
}

func (c *Client) SendFiles(ctx context.Context, channel, text, reply string, files []File) (string, error) {
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	payload := map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}}
	if reply != "" {
		payload["message_reference"] = map[string]any{"message_id": reply, "fail_if_not_exists": false}
	}
	part, err := writer.CreateFormField("payload_json")
	if err != nil {
		return "", err
	}
	if err := json.NewEncoder(part).Encode(payload); err != nil {
		return "", err
	}
	for i, file := range files {
		part, err := writer.CreateFormFile(fmt.Sprintf("files[%d]", i), file.Name)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(part, bytes.NewReader(file.Data)); err != nil {
			return "", err
		}
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	r, err := c.request(ctx, "POST", "/api/v10/channels/"+channel+"/messages", data.Bytes(), writer.FormDataContentType(), "")
	if err != nil {
		return "", err
	}
	if r.Status < 200 || r.Status >= 300 {
		return "", fmt.Errorf("Discord file send failed (HTTP %d)", r.Status)
	}
	var message struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.Body, &message) != nil || message.ID == "" {
		return "", errors.New("invalid Discord message response")
	}
	return message.ID, nil
}
