package engine

import (
	"context"
	"encoding/json"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type discordFileTransport struct{ base http.RoundTripper }

func (t discordFileTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Hostname() != "cdn.discordapp.com" {
		return t.base.RoundTrip(r)
	}
	status := 200
	body := "\x89PNG\r\n\x1a\nimage"
	if strings.Contains(r.URL.Path, "fail") {
		status = 404
	}
	header := make(http.Header)
	if strings.Contains(r.URL.Path, "redirect") {
		status = 302
		header.Set("Location", "https://example.invalid/private")
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header, Request: r}, nil
}
func TestDiscordInputFilesAndForward(t *testing.T) {
	f := discordFixture(t)
	f.s.SetLayout(box.Layout{Data: t.TempDir()})
	child := startChild(t, f.s, f.ctx, f.room, "reader", f.root)
	if err := f.s.store.SetDiscordRoute(f.ctx, store.DiscordRoute{Guild: "100", Room: f.room, Session: child}, false); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = discordFileTransport{old}
	t.Cleanup(func() { http.DefaultTransport = old })
	var m discord.Message
	json.Unmarshal([]byte(`{"id":"700","channel_id":"200","guild_id":"100","author":{"id":"1","username":"owner"},"attachments":[{"filename":"../../image.png","size":12,"url":"https://cdn.discordapp.com/image"},{"filename":"large","size":10485761,"url":"https://cdn.discordapp.com/large"},{"filename":"missing","size":1,"url":"https://cdn.discordapp.com/fail"}]}`), &m)
	if err := f.s.discordInput(f.ctx, f.client, m); err != nil {
		t.Fatal(err)
	}
	events, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events %v %v", events, err)
	}
	b := events[0].Body.(contract.DiscordMessage)
	if len(b.Files) != 1 || b.Files[0].Name != "image.png" || b.Files[0].Media != "image/png" || !strings.Contains(b.Attachments[1].Status, "skipped") || b.Attachments[2].Status != "not downloaded" {
		t.Fatalf("attachments %+v files %+v", b.Attachments, b.Files)
	}
	frame, files, err := f.s.inputFrame(f.ctx, child, events[0], contract.Delivery{})
	if err != nil || len(files) != 1 || !strings.Contains(frame, "shown to you") || !strings.Contains(frame, "outside content") {
		t.Fatalf("frame %s %v", frame, err)
	}
	// The post tool uploads these paths as the same hash-backed attachment descriptors.
	_, problem := f.s.Post(f.ctx, boxCaller(child), contract.PostRequest{Room: f.room, Text: "Image", Mentions: []string{"parent"}, Attachments: b.Files})
	if problem != nil {
		t.Fatal(problem)
	}
	posts, err := f.s.store.Events(f.ctx, f.room, 0, 100, []contract.EventType{"post.created"})
	if err != nil || len(posts) == 0 {
		t.Fatalf("posts %v", err)
	}
	frame, files, err = f.s.inputFrame(f.ctx, f.root, posts[len(posts)-1], contract.Delivery{})
	if err != nil || len(files) != 1 || files[0].SHA256 != b.Files[0].SHA256 || !strings.Contains(frame, "shown to you") {
		t.Fatalf("forward %s %v", frame, err)
	}
}
func TestDiscordRedirectRefused(t *testing.T) {
	f := discordFixture(t)
	f.s.SetLayout(box.Layout{Data: t.TempDir()})
	old := http.DefaultTransport
	http.DefaultTransport = discordFileTransport{old}
	t.Cleanup(func() { http.DefaultTransport = old })
	files, listed := f.s.downloadDiscordFiles(f.ctx, f.room, []contract.DiscordAttachment{{Name: "file", URL: "https://cdn.discordapp.com/redirect"}})
	if len(files) != 0 || listed[0].Status != "not downloaded" {
		t.Fatalf("redirect downloaded: %v %v", files, listed)
	}
}
func TestDiscordCDNBoundary(t *testing.T) {
	for _, u := range []string{"http://cdn.discordapp.com/x", "https://cdn.discordapp.com.evil/x", "https://evil/x", "https://cdn.discordapp.com:22/x", "https://user@cdn.discordapp.com/x"} {
		if discordCDN(u) {
			t.Fatal(u)
		}
	}
}

type blockedDiscordFiles struct{ started chan struct{} }

func (b blockedDiscordFiles) RoundTrip(r *http.Request) (*http.Response, error) {
	b.started <- struct{}{}
	<-r.Context().Done()
	return nil, r.Context().Err()
}
func TestDiscordDownloadsConcurrentAndCanceled(t *testing.T) {
	f := discordFixture(t)
	old := http.DefaultTransport
	started := make(chan struct{}, 10)
	http.DefaultTransport = blockedDiscordFiles{started}
	t.Cleanup(func() { http.DefaultTransport = old })
	var cancel context.CancelFunc
	f.ctx, cancel = context.WithTimeout(f.ctx, 100*time.Millisecond)
	defer cancel()
	attachments := make([]contract.DiscordAttachment, 10)
	for i := range attachments {
		attachments[i] = contract.DiscordAttachment{Name: "slow", URL: "https://cdn.discordapp.com/slow"}
	}
	files, listed := f.s.downloadDiscordFiles(f.ctx, f.room, attachments)
	if len(started) != 10 || len(files) != 0 {
		t.Fatalf("started %d files %d", len(started), len(files))
	}
	for _, a := range listed {
		if a.Status != "not downloaded" {
			t.Fatal(a)
		}
	}
}

func TestDiscordDownloadBudgetAndDiskFloor(t *testing.T) {
	f := discordFixture(t)
	f.s.SetLayout(box.Layout{Data: t.TempDir()})
	old := http.DefaultTransport
	http.DefaultTransport = discordFileTransport{old}
	t.Cleanup(func() { http.DefaultTransport = old })
	charge := f.s.reserveDiscordDownload(f.room, discordHourlyBytes)
	if charge == nil {
		t.Fatal("cannot reserve initial budget")
	}
	files, listed := f.s.downloadDiscordFiles(f.ctx, f.room, []contract.DiscordAttachment{{Name: "image", Size: 12, URL: "https://cdn.discordapp.com/image"}})
	if len(files) != 0 || listed[0].Status != discordHourlyLimit {
		t.Fatalf("budget %v %v", files, listed)
	}
	// Correction releases unused declared bytes; expired charges do not block a later hour.
	if !f.s.correctDiscordDownload(f.room, charge, 1) {
		t.Fatal("correction rejected")
	}
	files, _ = f.s.downloadDiscordFiles(f.ctx, f.room, []contract.DiscordAttachment{{Name: "image", Size: 12, URL: "https://cdn.discordapp.com/image"}})
	if len(files) != 1 {
		t.Fatal("unused reservation not released")
	}
	f.s.discordDownloadMu.Lock()
	for _, c := range f.s.discordDownloads[f.room] {
		c.at = f.s.now().Add(-2 * time.Hour)
	}
	f.s.discordDownloadMu.Unlock()
	if f.s.reserveDiscordDownload(f.room, discordHourlyBytes) == nil {
		t.Fatal("expired budget still charged")
	}
	yes := true
	if err := f.s.store.AddHost(f.ctx, box.Host{Name: "engine-disk", EngineHost: &yes, DiskBytes: 1, DiskFloor: 20}); err != nil {
		t.Fatal(err)
	}
	files, listed = f.s.downloadDiscordFiles(f.ctx, f.room, []contract.DiscordAttachment{{Name: "image", Size: 12, URL: "https://cdn.discordapp.com/image"}})
	if len(files) != 0 || listed[0].Status != "not downloaded: the engine's disk is low" {
		t.Fatalf("disk floor %v %v", files, listed)
	}
}
