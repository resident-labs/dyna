package engine

import (
	"bytes"
	"context"
	"fmt"
	"github.com/resident-labs/dyna/engine/discord"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func discordCDN(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") && (u.Hostname() == "cdn.discordapp.com" || u.Hostname() == "media.discordapp.net")
}

// downloadDiscordFiles treats all senders, including the owner, as untrusted.
func (s *Service) downloadDiscordFiles(ctx context.Context, room contract.RoomID, attachments []contract.DiscordAttachment) ([]contract.Attachment, []contract.DiscordAttachment) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !discordCDN(req.URL.String()) {
			return fmt.Errorf("refused attachment redirect")
		}
		return nil
	}}
	hosts, diskErr := s.store.Hosts(ctx)
	diskLow := false
	for _, h := range hosts {
		if h.EngineHost != nil && *h.EngineHost && box.DiskLow(h.DiskBytes, h.DiskFloor) {
			diskLow = true
		}
	}
	downloaded := make([]*contract.Attachment, len(attachments))
	var wg sync.WaitGroup
	for i := range attachments {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := &attachments[i]
			a.Name = path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
			if a.Name == "." || a.Name == "/" || a.Name == "" {
				a.Name = "attachment"
			}
			a.Status = "not downloaded"
			if i >= 10 {
				a.Status = "skipped: at most 10 files"
				return
			}
			if a.Size > 10<<20 {
				a.Status = "skipped: larger than 10 MB"
				return
			}
			if !discordCDN(a.URL) {
				return
			}
			if diskErr != nil {
				return
			}
			if diskLow {
				a.Status = "not downloaded: the engine's disk is low"
				return
			}
			charge := s.reserveDiscordDownload(room, a.Size)
			if charge == nil {
				a.Status = discordHourlyLimit
				return
			}
			actual := int64(0)
			defer func() { s.correctDiscordDownload(room, charge, actual) }()
			req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
			if err != nil {
				return
			}
			res, err := client.Do(req)
			if err != nil {
				return
			}
			data, readErr := io.ReadAll(io.LimitReader(res.Body, (10<<20)+1))
			res.Body.Close()
			if res.StatusCode != http.StatusOK || readErr != nil {
				return
			}
			if len(data) > 10<<20 {
				a.Status = "skipped: larger than 10 MB"
				return
			}
			actual = int64(len(data))
			if !s.correctDiscordDownload(room, charge, actual) {
				actual = 0
				a.Status = discordHourlyLimit
				return
			}
			hash, n, err := s.saveFile(ctx, room, bytes.NewReader(data), 10<<20)
			if err != nil {
				return
			}
			downloaded[i] = &contract.Attachment{SHA256: hash, Name: a.Name, Bytes: n, Media: http.DetectContentType(data)}
			a.Status = "downloaded"
		}(i)
	}
	wg.Wait()
	var files []contract.Attachment
	for _, file := range downloaded {
		if file != nil {
			files = append(files, *file)
		}
	}
	return files, attachments
}

func (s *Service) discordReadFiles(ctx context.Context, caller contract.Principal, m discord.Message) (string, *contract.Error) {
	session, err := s.store.Session(ctx, caller.Session)
	if err != nil {
		return "", failure(err)
	}
	b := contract.DiscordFilesRetrieved{Channel: m.Channel, Message: m.ID, Author: m.Author.Username, AuthorID: m.Author.ID}
	if channel, ok := s.discordDirectory.Channel(m.Channel); ok {
		b.ChannelName = channel.Name
	}
	for _, a := range m.Files() {
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
	event.Seq, err = s.store.Post(ctx, event, []contract.Delivery{{To: caller, Surface: contract.SurfaceEngine, Wake: true, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}}})
	if err != nil {
		return "", failure(err)
	}
	_, described := s.attachedFiles(event, b.Files)
	var statuses []string
	for _, a := range b.Attachments {
		statuses = append(statuses, fmt.Sprintf("%s: %s", a.Name, a.Status))
	}
	return "Files arrive as the next input of this turn: " + described + "\n" + strings.Join(statuses, "\n"), nil
}
