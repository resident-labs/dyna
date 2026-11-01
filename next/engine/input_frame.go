package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// maxShownImage is the largest attachment shown to the model as an image. Pi shrinks what it can below the model's limit,
// and this keeps a huge file from reaching that step. A larger image is a file like any other.
const maxShownImage = 16 << 20

func (s *Service) framePrincipal(ctx context.Context, viewer contract.SessionID, principal contract.Principal) (string, error) {
	switch principal.Kind {
	case contract.PrincipalOwner:
		return "the owner", nil
	case contract.PrincipalEngine:
		return "the Dyna engine", nil
	case contract.PrincipalSession:
		name, parent, viewerParent, err := s.store.FrameSession(ctx, viewer, principal.Session)
		if err != nil {
			return "", err
		}
		name = fmt.Sprintf("%q", name)
		if principal.Session == viewerParent {
			name = "your parent " + name
		} else if parent == viewer {
			name = "your child " + name
		}
		return name, nil
	}
	return "the Dyna engine", nil
}

// attachedFiles lists a post's files for the box and describes them for the frame. The Pi extension reads "shown to you" in
// this text to put the image in the model's requests, so box/agent/extension.js changes with it.
func (s *Service) attachedFiles(event contract.Event, post contract.PostCreated) ([]contract.TurnFile, string) {
	paths := s.layout.BoxAttachments(event.Seq, post.Attachments)
	files := make([]contract.TurnFile, len(paths))
	described := make([]string, len(paths))
	for i, file := range post.Attachments {
		files[i] = contract.TurnFile{Path: paths[i], SHA256: file.SHA256, Bytes: file.Bytes}
		media, detail := file.Media, ""
		// The bytes decide: a provider refuses an image that is not what its label says, and goes on refusing the conversation.
		if image := s.sniffImage(event.Room, file.SHA256); image != "" {
			media, detail = image, ", shown to you"
			if file.Bytes > maxShownImage {
				detail = ", too large to show you, so read the file"
			}
		}
		described[i] = fmt.Sprintf("%s (%s, %s%s)", paths[i], media, fileSize(file.Bytes), detail)
	}
	return files, strings.Join(described, ", ")
}

// sniffImage returns the media type of a blob that a model can take as an image, or "" for anything else.
func (s *Service) sniffImage(room contract.RoomID, hash string) string {
	f, err := os.Open(s.layout.File(room, hash))
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return ""
	}
	switch media := http.DetectContentType(head[:n]); media {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return media
	}
	return ""
}

func fileSize(bytes int64) string {
	if bytes < 1<<20 {
		return fmt.Sprintf("%d KB", (bytes+1023)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
}

func (s *Service) inputFrame(ctx context.Context, viewer contract.SessionID, event contract.Event, delivery contract.Delivery) (string, []contract.TurnFile, error) {
	sender, err := s.framePrincipal(ctx, viewer, event.From)
	if err != nil {
		return "", nil, err
	}
	// A Run's post comes through the engine, but a script wrote it, so the frame must not vouch for it.
	if post, ok := event.Body.(contract.PostCreated); ok && post.Hook != "" {
		sender = fmt.Sprintf("the script of subscription %s", post.Hook)
	}
	var surface string
	switch delivery.Surface {
	case contract.SurfaceFeed:
		surface = "in the room feed"
	case contract.SurfaceDM:
		surface = "in your DM"
	case contract.SurfaceSession:
		surface = "as a session brief or result"
	case contract.SurfaceCard:
		surface = "through a card"
	case contract.SurfaceEngine:
		surface = "as a system notice"
	case contract.SurfaceService:
		surface = "in the room feed"
	}
	frame := fmt.Sprintf("[Dyna] From %s, %s.", sender, surface)
	if delivery.HandoffFrom != "" {
		origin, err := s.framePrincipal(ctx, viewer, contract.Principal{Kind: contract.PrincipalSession, Session: delivery.HandoffFrom})
		if err != nil {
			return "", nil, err
		}
		frame += fmt.Sprintf(" This input was handed over when %s closed.", origin)
	}
	var files []contract.TurnFile
	if post, ok := event.Body.(contract.PostCreated); ok && len(post.Attachments) > 0 {
		var described string
		files, described = s.attachedFiles(event, post)
		frame += " Attached: " + described + "."
	}
	switch delivery.Reply.Kind {
	case contract.ReplyFinal:
		target, err := s.framePrincipal(ctx, viewer, delivery.Reply.Target)
		if err != nil {
			return "", nil, err
		}
		home := "in the feed"
		if delivery.Reply.Home.Kind == contract.ConversationDM {
			home = "in your DM"
		}
		frame += fmt.Sprintf(" Your final response goes to %s %s.", target, home)
	case contract.ReplyPost:
		frame += " No reply is delivered."
	case contract.ReplyNone:
		frame += " No reply is expected."
	}
	return frame, files, nil
}
