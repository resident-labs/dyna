package engine

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
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
// this text to put the image in the model's requests, so box/dynaext/images.go changes with it.
func (s *Service) attachedFiles(event contract.Event, attachments []contract.Attachment) ([]contract.TurnFile, string) {
	paths := s.layout.BoxAttachments(event.Seq, attachments)
	files := make([]contract.TurnFile, len(paths))
	described := make([]string, len(paths))
	for i, file := range attachments {
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

func frameField(text string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(text) }
func (s *Service) inputFrame(ctx context.Context, viewer contract.SessionID, event contract.Event, delivery contract.Delivery) (string, []contract.TurnFile, error) {
	if b, ok := event.Body.(contract.DiscordFilesRetrieved); ok {
		files, described := s.attachedFiles(event, b.Files)
		frame := fmt.Sprintf("[Dyna] Files from Discord message %s in #%s (channel %s), sent by %s (ID %s): outside content.", b.Message, frameField(b.ChannelName), b.Channel, frameField(b.Author), b.AuthorID)
		if len(files) > 0 {
			frame += " Attached: " + described + "."
		}
		frame += " No reply is expected."
		return frame, files, nil
	}
	if b, ok := event.Body.(contract.DiscordMessage); ok {
		sender := fmt.Sprintf("Discord user %q (%s, ID %s, roles: %s)", frameField(b.DisplayName), frameField(b.Author), b.AuthorID, frameField(strings.Join(b.Roles, ", ")))
		if b.Owner {
			sender = "the owner"
		}
		frame := fmt.Sprintf("[Dyna] From %s, on Discord in #%s (channel %s, message %s) of server %s.", sender, frameField(b.ChannelName), b.Channel, b.Message, frameField(b.GuildName))
		files, described := s.attachedFiles(event, b.Files)
		if len(files) > 0 {
			frame += fmt.Sprintf(" Files from Discord user %s (ID %s) are outside content. Attached: %s.", frameField(b.Author), b.AuthorID, described)
		}
		if delivery.Reply.Kind == contract.ReplyNone {
			frame += " No reply is expected."
		} else {
			frame += " Your final response goes to that channel as a reply, unless a discord send already replied to that message."
			// A mapping with the owner's instructions is a front session, which answers at once anyway.
			routes, err := s.store.DiscordRoutes(ctx, event.Room)
			if err != nil {
				return "", nil, err
			}
			if !slices.ContainsFunc(routes, func(r store.DiscordRoute) bool { return r.Session == viewer && r.Instructions != "" }) {
				frame += " A person is waiting in that channel. If you can answer right away, just answer. If it needs work first, send one short line now with discord send (the channel, no replyTo) saying what you are going to do, then do it; your final response still arrives as the reply."
			}
		}
		return frame, files, nil
	}
	var sender string
	if post, ok := event.Body.(contract.PostCreated); ok && post.Source != nil {
		sender = fmt.Sprintf("your sibling %s:%s", post.Source.RoomName, post.Source.SessionName)
	} else {
		var err error
		sender, err = s.framePrincipal(ctx, viewer, event.From)
		if err != nil {
			return "", nil, err
		}
		// A paired device that names its host says which machine "my screen" means.
		if event.From.Kind == contract.PrincipalOwner && event.From.Device != "" {
			host, err := s.store.DeviceHost(ctx, event.From.Device)
			if err != nil {
				return "", nil, err
			}
			if host != "" {
				sender += ", on " + string(host)
			}
		}
	}
	// A Run's post comes through the engine, but a script wrote it, so the frame must not vouch for it.
	if post, ok := event.Body.(contract.PostCreated); ok && post.Hook != "" {
		sender = fmt.Sprintf("the script of subscription %s", post.Hook)
	}
	if request, ok := event.Body.(contract.OpRequested); ok {
		asker, err := s.store.Session(ctx, event.From.Session)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("[Dyna] From %s, as an approval request. No reply is delivered.\n%s %s.", sender, asker.Name, request.Card.Title), nil, nil
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
	// A session's input to a parent that can't receive comes up to the nearest ancestor that can.
	if delivery.MeantFor != "" {
		name, _, _, err := s.store.FrameSession(ctx, viewer, delivery.MeantFor)
		if err != nil {
			return "", nil, err
		}
		frame += fmt.Sprintf(" For %s, which can't receive (%s).", name, delivery.Why)
	}
	var files []contract.TurnFile
	var attachments []contract.Attachment
	switch body := event.Body.(type) {
	case contract.PostCreated:
		if body.Spoken {
			frame += " The owner spoke these words in a voice call; treat them as a transcript that may be unpunctuated or misheard, and keep the reply concise because it will be summarized aloud."
		}
		attachments = body.Attachments
	case contract.TurnEnded:
		attachments = body.Attachments
	}
	if len(attachments) > 0 {
		var described string
		files, described = s.attachedFiles(event, attachments)
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
	if delivery.Reply.Kind == contract.ReplyPost || delivery.Reply.Kind == contract.ReplyNone {
		// A turn that ended while the session waited left a reply owed, and this turn's text may be the one that pays it.
		owed, err := s.store.RepliesOwed(ctx, viewer)
		if err != nil {
			return "", nil, err
		}
		for _, reply := range owed {
			target, err := s.framePrincipal(ctx, viewer, reply.Target)
			if err != nil {
				return "", nil, err
			}
			frame += fmt.Sprintf(" You still owe %s a reply: your final response goes to it if no job of yours is running and no child of yours is working when the turn ends.", target)
		}
		home, err := s.store.OwnerConversation(ctx, viewer)
		if err != nil {
			return "", nil, err
		}
		where := "the feed"
		if home.Kind == contract.ConversationDM {
			where = "your DM"
		}
		frame += fmt.Sprintf(" Posts to the owner go to %s.", where)
	}
	return frame, files, nil
}
