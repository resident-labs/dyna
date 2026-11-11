package engine

import (
	"encoding/json"
	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/discord"
	"github.com/resident-labs/dyna/engine/store"
	"io"
	"net/http"
	"strings"
	"testing"
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
	s, r, root, ctx, _, client := discordFixture(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	child := startChild(t, s, ctx, r, "reader", root)
	if err := s.store.SetDiscordRoute(ctx, store.DiscordRoute{Guild: "100", Room: r, Session: child}, false); err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	http.DefaultTransport = discordFileTransport{old}
	t.Cleanup(func() { http.DefaultTransport = old })
	var m discord.Message
	json.Unmarshal([]byte(`{"id":"700","channel_id":"200","guild_id":"100","author":{"id":"1","username":"owner"},"attachments":[{"filename":"../../image.png","size":12,"url":"https://cdn.discordapp.com/image"},{"filename":"large","size":10485761,"url":"https://cdn.discordapp.com/large"},{"filename":"missing","size":1,"url":"https://cdn.discordapp.com/fail"}]}`), &m)
	if err := s.discordInput(ctx, client, m); err != nil {
		t.Fatal(err)
	}
	events, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"discord.message"})
	if err != nil || len(events) != 1 {
		t.Fatalf("events %v %v", events, err)
	}
	b := events[0].Body.(contract.DiscordMessage)
	if len(b.Files) != 1 || b.Files[0].Name != "image.png" || b.Files[0].Media != "image/png" || !strings.Contains(b.Attachments[1].Status, "skipped") || b.Attachments[2].Status != "not downloaded" {
		t.Fatalf("attachments %+v files %+v", b.Attachments, b.Files)
	}
	frame, files, err := s.inputFrame(ctx, child, events[0], contract.Delivery{})
	if err != nil || len(files) != 1 || !strings.Contains(frame, "shown to you") || !strings.Contains(frame, "outside content") {
		t.Fatalf("frame %s %v", frame, err)
	}
	// The post tool uploads these paths as the same hash-backed attachment descriptors.
	_, problem := s.Post(ctx, boxCaller(child), contract.PostRequest{Room: r, Text: "Image", Mentions: []string{"parent"}, Attachments: b.Files})
	if problem != nil {
		t.Fatal(problem)
	}
	posts, err := s.store.Events(ctx, r, 0, 100, []contract.EventType{"post.created"})
	if err != nil || len(posts) == 0 {
		t.Fatalf("posts %v", err)
	}
	frame, files, err = s.inputFrame(ctx, root, posts[len(posts)-1], contract.Delivery{})
	if err != nil || len(files) != 1 || files[0].SHA256 != b.Files[0].SHA256 || !strings.Contains(frame, "shown to you") {
		t.Fatalf("forward %s %v", frame, err)
	}
}
func TestDiscordRedirectRefused(t *testing.T) {
	s, r, _, ctx, _, _ := discordFixture(t)
	s.SetLayout(box.Layout{Data: t.TempDir()})
	old := http.DefaultTransport
	http.DefaultTransport = discordFileTransport{old}
	t.Cleanup(func() { http.DefaultTransport = old })
	files, listed := s.downloadDiscordFiles(ctx, r, []contract.DiscordAttachment{{Name: "file", URL: "https://cdn.discordapp.com/redirect"}})
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
