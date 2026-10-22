package room

import (
	"errors"
	"fmt"
	"strings"
)

// Shot is a screenshot of a card at one viewport width.
type Shot struct {
	Width int    `json:"width"`
	ID    string `json:"id"`
}

// Check is one automatic quality check of a card.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass, warn, fail or skip
	Detail string `json:"detail,omitempty"`
}

const (
	maxShots       = 3
	maxChecks      = 8
	maxCheckDetail = 200
)

// checkPreview accepts a card's preview only when every image is a saved
// image in the room and every check is short and well formed.
func (s *Store) checkPreview(roomID string, a Attachment) error {
	if len(a.Shots) > maxShots || len(a.Checks) > maxChecks {
		return errors.New("a card preview has too many screenshots or checks")
	}
	ids := []string{a.Banner, a.Thumb}
	for _, shot := range a.Shots {
		if shot.Width <= 0 {
			return errors.New("a card screenshot needs its width")
		}
		ids = append(ids, shot.ID)
	}
	for _, id := range ids {
		if id == "" {
			continue
		}
		path, err := s.AttachmentPath(roomID, id)
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, ".webp") && !strings.HasSuffix(path, ".png") {
			return fmt.Errorf("card preview %s is not an image", id)
		}
	}
	for _, c := range a.Checks {
		switch c.Status {
		case "pass", "warn", "fail", "skip":
		default:
			return fmt.Errorf("unknown check status %q", c.Status)
		}
		if c.Name == "" || len(c.Name) > 40 || len(c.Detail) > maxCheckDetail {
			return errors.New("a card check needs a short name and detail")
		}
	}
	return nil
}

// CardCSP is the Content-Security-Policy of a card. Agent HTML is
// untrusted. A card runs in <iframe sandbox="allow-scripts"> with an opaque
// origin: no app storage, no same-origin requests, no forms, popups, modals
// or top navigation. The desktop loads the same web app and has no native
// API bindings. Inline scripts and styles always run. With net (dyna post
// --allow-net) the card may load scripts and data over HTTPS, plus data:
// and blob:; plain http: would reach localhost and LAN devices. Without net it loads only
// data: and blob:. Each fetch directive is named, not left to default-src.
// frameAncestors names the app origins that may embed the card.
func CardCSP(net bool, frameAncestors string) string {
	src, script := "data: blob:", "'unsafe-inline'"
	if net {
		src, script = "https: data: blob:", "'unsafe-inline' https:"
	}
	return "default-src 'none'; script-src " + script + "; " +
		"connect-src " + src + "; img-src " + src + "; font-src " + src + "; " +
		"media-src " + src + "; style-src 'unsafe-inline' " + src + "; " +
		"frame-src 'none'; child-src 'none'; object-src 'none'; worker-src 'none'; " +
		"base-uri 'none'; form-action 'none'; frame-ancestors " + frameAncestors + "; sandbox allow-scripts"
}
