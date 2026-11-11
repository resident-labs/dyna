package engine

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"github.com/resident-labs/dyna/engine/discord"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func discordCDN(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && (u.Hostname() == "cdn.discordapp.com" || u.Hostname() == "media.discordapp.net")
}

// downloadDiscordFiles treats all senders, including the owner, as untrusted.
func (s *Service) downloadDiscordFiles(ctx context.Context, room contract.RoomID, attachments []contract.DiscordAttachment) ([]contract.Attachment, []contract.DiscordAttachment) {
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !discordCDN(req.URL.String()) {
			return fmt.Errorf("refused attachment redirect")
		}
		return nil
	}}
	var files []contract.Attachment
	for i := range attachments {
		a := &attachments[i]
		a.Name = path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
		if a.Name == "." || a.Name == "/" || a.Name == "" {
			a.Name = "attachment"
		}
		a.Status = "not downloaded"
		if i >= 10 {
			a.Status = "skipped: at most 10 files"
			continue
		}
		if a.Size > 10<<20 {
			a.Status = "skipped: larger than 10 MB"
			continue
		}
		if !discordCDN(a.URL) {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
		if err != nil {
			continue
		}
		res, err := client.Do(req)
		if err != nil {
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, (10<<20)+1))
		res.Body.Close()
		if res.StatusCode != http.StatusOK || readErr != nil {
			continue
		}
		if len(data) > 10<<20 {
			a.Status = "skipped: larger than 10 MB"
			continue
		}
		hash, n, err := s.saveFile(ctx, room, bytes.NewReader(data), 10<<20)
		if err != nil {
			continue
		}
		files = append(files, contract.Attachment{SHA256: hash, Name: a.Name, Bytes: n, Media: http.DetectContentType(data)})
		a.Status = "downloaded"
	}
	return files, attachments
}

func (s *Service) discordReadFiles(ctx context.Context, caller contract.Principal, m discord.Message) (string, *contract.Error) {
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return "", failure(err)
	}
	b := contract.DiscordMessage{Channel: m.Channel, Message: m.ID, Author: m.Author.Username, AuthorID: m.Author.ID, DisplayName: m.Author.GlobalName, Text: "Attachments retrieved by discord read."}
	for _, a := range m.Attachments {
		b.Attachments = append(b.Attachments, contract.DiscordAttachment{Name: a.Name, Size: a.Size, URL: a.URL})
	}
	if len(b.Attachments) == 0 {
		return "", nil
	}
	b.Files, b.Attachments = s.downloadDiscordFiles(ctx, session.Room, b.Attachments)
	event, err := s.event(session.Room, contract.Principal{Kind: contract.PrincipalEngine}, b)
	if err != nil {
		return "", failure(err)
	}
	err = s.store.Write(ctx, func(tx *sql.Tx) error {
		seq, err := s.store.AppendEvent(tx, event)
		if err != nil {
			return err
		}
		event.Seq = seq
		return s.store.InsertDelivery(ctx, tx, seq, b, contract.Delivery{To: caller, Surface: contract.SurfaceDiscord, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}})
	})
	if err != nil {
		return "", failure(err)
	}
	_, described := s.attachedFiles(event, b.Files)
	var statuses []string
	for _, a := range b.Attachments {
		statuses = append(statuses, fmt.Sprintf("%s: %s", a.Name, a.Status))
	}
	return "Attachment delivery queued: " + described + "\n" + strings.Join(statuses, "\n"), nil
}
