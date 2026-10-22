package filesvc

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"dyna-agent/internal/room"
)

// fileCSP serves room bytes that are never HTML. Cards use room.CardCSP.
const (
	fileCSP   = "default-src 'none'; sandbox"
	immutable = "max-age=31536000, immutable"
)

// ErrNotRich is returned for an attachment no post in the room marks as an
// HTML card.
var ErrNotRich = errors.New("no post in this room shows this attachment as an HTML card")

// RichDocument returns the bytes of an HTML card. It reads the stored post
// records, never the file type: `--attach page.html` must not render. Bytes
// are content addressed, so the same file can be plain in one post and a
// card in another. Then /rich/ serves it (same ID), and each post still
// shows it the way its own record says. net reports that the newest post
// showing it allowed the network.
func (s *Service) RichDocument(roomRef, id string) ([]byte, bool, error) {
	rec, err := s.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, false, err
	}
	msgs, err := s.Rooms.Messages(rec.ID)
	if err != nil {
		return nil, false, err
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, a := range msgs[i].Attachments {
			if a.ID == id && a.Rich() {
				b, err := s.AttachmentBytes(rec.ID, id)
				if err != nil {
					return nil, false, err
				}
				return b, a.Net, nil
			}
		}
	}
	return nil, false, ErrNotRich
}

// cardAutoHeight follows every card. It reports the document's height to
// the feed with the same dyna:height message a card may send itself, so a
// card shows at its full height in the chat with no scrollbar of its own.
// Hide the vertical scrollbar only when the document fits its viewport.
// Expanded viewers, question previews and capped feed cards can be shorter
// than the document and must keep native scrolling in their own frame.
// It runs only when the card may run scripts; otherwise the feed keeps the
// card's declared or default height.
//
// A card sized to its viewport (100vh, or height:100% with a horizontal
// scrollbar) grows each time the frame grows. When the frame already has
// the last height sent and the document outgrows it three times in two
// seconds, the script stops following and the card scrolls inside its
// frame instead.
const cardAutoHeight = `<script>(function(){var last=0,hits=[],off=false,o;function send(){if(off)return;var d=document.documentElement,b=document.body;` +
	`var h=Math.ceil(Math.max(d.getBoundingClientRect().height,b?b.scrollHeight:0)+(innerHeight-d.clientHeight));` +
	`if(last&&h>last&&innerHeight>=last){var t=Date.now();hits=hits.filter(function(x){return t-x<2000});hits.push(t);` +
	`if(hits.length>=3){off=true;if(o)o.disconnect();d.style.overflowY="";return}}` +
	`if(h>0&&h!==last){last=h;parent.postMessage({type:"dyna:height",height:h},"*")}` +
	`d.style.overflowY=h<=innerHeight?"hidden":"auto"}` +
	`if(window.ResizeObserver){o=new ResizeObserver(send);o.observe(document.documentElement);if(document.body)o.observe(document.body)}` +
	`addEventListener("resize",send);addEventListener("load",send);send()})();</script>`

// FilesHandler serves room bytes over plain HTTP, so a desktop webview and a
// future browser client load them the same way:
//
//	/attachments/<room-id>/<attachment-id>  a file, never as HTML
//	/rich/<room-id>/<attachment-id>         an HTML card, locked down
//
// The IDs come from posts every room member can read. They are not secrets
// and grant nothing; a remote server must authorize these routes itself.
func (s *Service) FilesHandler() http.Handler { return s.filesHandler("'self'") }

// FilesHandlerFor is FilesHandler for a card origin apart from the app
// (internal/websvc): frameAncestors is the fixed, space-separated list of
// app origins that may embed a card, never a value taken from a request.
func (s *Service) FilesHandlerFor(frameAncestors string) http.Handler {
	return s.filesHandler(frameAncestors)
}

func (s *Service) filesHandler(frameAncestors string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Only found bytes may be cached: they are content addressed. A 404
		// must not be pinned for a year.
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		// Workspace images share the attachment route and its response policy.
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) == 3 && parts[0] == "attachments" && parts[2] == "workspace" {
			h.Set("Content-Security-Policy", fileCSP)
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.NotFound(w, r)
				return
			}
			root, err := s.WorkspaceRoot(parts[1], r.URL.Query().Get("session"))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			b, _, truncated, err := readWorkspaceBytes(WorkspacePath(root, r.URL.Query().Get("path")))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			if truncated {
				http.Error(w, "file exceeds the 2 MiB image preview limit", http.StatusRequestEntityTooLarge)
				return
			}
			h.Set("Content-Type", fileType(b))
			h.Set("Content-Length", strconv.Itoa(len(b)))
			if r.Method == http.MethodGet {
				_, _ = w.Write(b)
			}
			return
		}
		route, roomID, id, ok := splitFilePath(r.URL.Path)
		if !ok || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.NotFound(w, r)
			return
		}
		if route == "rich" {
			b, net, err := s.RichDocument(roomID, id)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			// Not immutable: the newest post decides the policy, and the same
			// bytes can be posted again with or without --allow-net.
			h.Set("Cache-Control", "no-store")
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Content-Security-Policy", room.CardCSP(net, frameAncestors))
			_, _ = w.Write(b)
			_, _ = io.WriteString(w, cardAutoHeight)
			return
		}
		b, err := s.AttachmentBytes(roomID, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		h.Set("Cache-Control", immutable)
		h.Set("Content-Type", fileType(b))
		h.Set("Content-Security-Policy", fileCSP)
		_, _ = w.Write(b)
	})
}

// fileType names served bytes. Only the inline image types keep their
// type; text is plain and everything else is opaque, so no attachment can
// run as a page on the app's origin.
func fileType(b []byte) string {
	t := http.DetectContentType(b)
	switch base, _, _ := strings.Cut(t, ";"); base {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return base
	}
	if strings.HasPrefix(t, "text/") {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

func splitFilePath(p string) (route, roomID, id string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) != 3 || (parts[0] != "attachments" && parts[0] != "rich") {
		return "", "", "", false
	}
	if !room.ValidID(parts[1]) || !room.ValidAttachmentID(parts[2]) {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}
