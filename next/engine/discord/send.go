package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/resident-labs/dyna/room"
	"io"
	"mime/multipart"
	"time"
)

// Split preserves the public transport helper while sharing formatting with EndTurn.
func Split(text string) []string { return room.SplitDiscord(text) }

type SendError struct {
	Status     int
	RetryAfter time.Duration
}

func (e *SendError) Error() string { return fmt.Sprintf("Discord send failed (HTTP %d)", e.Status) }

func (c *Client) Send(ctx context.Context, channel, text, reply string) ([]string, int, error) {
	var ids []string
	for _, part := range Split(text) {
		id, status, err := c.SendMessage(ctx, channel, part, reply)
		if err != nil {
			return ids, status, err
		}
		ids = append(ids, id)
		reply = ""
	}
	return ids, 200, nil
}

// SendMessage sends one already-sized message, as used by the durable outbox.
func (c *Client) SendMessage(ctx context.Context, channel, text, reply string) (string, int, error) {
	body := map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{"users"}}}
	if reply != "" {
		body["message_reference"] = map[string]any{"message_id": reply, "fail_if_not_exists": false}
	}
	data, _ := json.Marshal(body)
	r, err := c.API(ctx, "POST", fmt.Sprintf("/api/v10/channels/%s/messages", channel), data, "")
	if err != nil {
		return "", 0, err
	}
	if r.Status < 200 || r.Status >= 300 {
		return "", r.Status, &SendError{Status: r.Status, RetryAfter: r.RetryAfter}
	}
	var message struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(r.Body, &message) != nil || message.ID == "" {
		return "", r.Status, errors.New("invalid Discord message response")
	}
	return message.ID, r.Status, nil
}

type File struct {
	Name string
	Data []byte
}

func (c *Client) SendFiles(ctx context.Context, channel, text, reply string, files []File) (string, error) {
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	payload := map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{"users"}}}
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
