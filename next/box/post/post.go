// Package post prepares local files before sending an engine post.
package post

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/resident-labs/dyna/box/visual"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/contract/tools"
)

type Upload func(context.Context, string) (contract.Attachment, error)

// Preparer uploads local files and renders visuals before posting.
type Preparer struct {
	Upload Upload
	Render func(context.Context, []byte, string, bool) visual.Result
	Dir    string
}

func (p Preparer) Prepare(ctx context.Context, in tools.PostInput) (contract.PostRequest, []contract.VisualCheck, error) {
	req := contract.PostRequest{Room: in.Room, Text: in.Text, Mentions: in.Mentions, Home: in.Home, ReplyTo: in.ReplyTo, ClientID: in.ClientID, VisualUpdate: in.VisualUpdate, VisualReply: in.VisualReply}
	if (in.VisualUpdate != "" || in.AllowNet) && in.HTML == "" {
		return req, nil, fmt.Errorf("visualUpdate and allowNet need html")
	}
	if in.HTML != "" && strings.TrimSpace(in.Text) == "" {
		return req, nil, fmt.Errorf("html needs post text")
	}
	count := len(in.Paths)
	if in.HTML != "" {
		count++
	}
	if count > 16 {
		return req, nil, fmt.Errorf("a post can carry at most 16 attachments")
	}
	for _, path := range in.Paths {
		if !filepath.IsAbs(path) {
			path = filepath.Join(p.Dir, path)
		}
		file, err := p.Upload(ctx, path)
		if err != nil {
			return req, nil, err
		}
		req.Attachments = append(req.Attachments, file)
	}
	if in.HTML == "" {
		return req, nil, nil
	}
	htmlPath := in.HTML
	if !filepath.IsAbs(htmlPath) {
		htmlPath = filepath.Join(p.Dir, htmlPath)
	}
	data, err := visual.ReadFile(htmlPath)
	if err != nil {
		return req, nil, err
	}
	title, height, err := visual.Meta(in.HTML, data)
	if err != nil {
		return req, nil, err
	}
	card := &contract.VisualCard{Title: title, Height: height, AllowNet: in.AllowNet}
	preview := p.Render(ctx, data, visual.CSP(in.AllowNet), in.AllowNet)
	if err = visualImages(ctx, p.Upload, preview, card); err != nil {
		return req, nil, err
	}
	dir, err := os.MkdirTemp("", "dyna-card-")
	if err != nil {
		return req, nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, filepath.Base(in.HTML))
	if err = os.WriteFile(path, data, 0600); err != nil {
		return req, nil, err
	}
	file, err := p.Upload(ctx, path)
	if err != nil {
		return req, nil, err
	}
	file.Visual = card
	req.Attachments = append(req.Attachments, file)
	return req, preview.Checks, nil
}
func visualImages(ctx context.Context, uploadFile Upload, result visual.Result, card *contract.VisualCard) error {
	dir, err := os.MkdirTemp("", "dyna-visual-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	upload := func(name string, data []byte) (string, error) {
		if len(data) == 0 {
			return "", nil
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			return "", err
		}
		file, err := uploadFile(ctx, path)
		return file.SHA256, err
	}
	if card.Banner, err = upload("banner.webp", result.Banner); err != nil {
		return err
	}
	if card.Thumbnail, err = upload("thumbnail.webp", result.Thumb); err != nil {
		return err
	}
	for _, shot := range result.Widths {
		file, err := upload(fmt.Sprintf("%d.webp", shot.Width), shot.Image)
		if err != nil {
			return err
		}
		card.Shots = append(card.Shots, contract.VisualShot{Width: shot.Width, File: file})
	}
	card.Checks = result.Checks
	return nil
}

// UploadHTTP sends a local file over the caller's authenticated transport. The owner names the room; a box's socket already fixes it.
func UploadHTTP(ctx context.Context, client *http.Client, room contract.RoomID, path string) (contract.Attachment, error) {
	var attachment contract.Attachment
	file, err := os.Open(path)
	if err != nil {
		return attachment, err
	}
	defer file.Close()
	name := filepath.Base(path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://dyna/v1/files?name="+url.QueryEscape(name)+"&room="+url.QueryEscape(string(room)), file)
	if err != nil {
		return attachment, err
	}
	media := mime.TypeByExtension(filepath.Ext(name))
	if media == "" {
		media = "application/octet-stream"
	}
	req.Header.Set("Content-Type", media)
	resp, err := client.Do(req)
	if err != nil {
		return attachment, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return attachment, fmt.Errorf("upload %s: %s", name, resp.Status)
	}
	err = json.NewDecoder(resp.Body).Decode(&attachment)
	return attachment, err
}
