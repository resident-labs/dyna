package dynaext

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/host/subprocess"
	"github.com/MichaelKinsy/PiG/extensions/sdk"
	"golang.org/x/sys/unix"

	"github.com/resident-labs/dyna/box"
)

// What one model request can carry in images. The model route takes 32 MiB in all, and the rest is for the conversation.
const (
	imageLimit      = 20
	imageBytesLimit = 20 << 20
)

// framedImage is an image the engine's frame shows the model: see attachedFiles in engine/input_frame.go.
var framedImage = regexp.MustCompile(`(/\S+/\d+/\S+) \((image/\S+), [^,)]+, shown to you\)`)

// shown is one attachment as a request shows it: the image, or the note that takes its place, or both.
type shown struct {
	image *ai.ImageContent
	note  string
}

// imageContext adds the images of the inputs' frames to each request. PiG's session file keeps only each input's
// frame, so an image adds nothing to the transcript and stays visible across a restart. Each request takes the images
// back from their files, newest first, up to the limits; an image left out can still be read as a file.
func imageContext() extension.ExtensionHandler[extension.ContextEvent, extension.ContextEventResult] {
	loaded := map[string]shown{}
	return func(ctx context.Context, e extension.ContextEvent) (extension.ContextEventResult, error) {
		var model *ai.Model
		if c := extension.FromContext(ctx); c != nil {
			model, _ = c.Model()
		}
		kept := map[string]shown{}
		images, bytes := 0, 0
		messages := slices.Clone(e.Messages)
		for i, message := range slices.Backward(messages) {
			if message.User == nil {
				continue
			}
			blocks := userBlocks(message.User.Content)
			if len(blocks) == 0 {
				continue
			}
			text, ok := blocks[0].(ai.TextContent)
			if !ok || !strings.HasPrefix(text.Text, "[Dyna] ") {
				continue
			}
			frame, _, _ := strings.Cut(text.Text, "\n")
			var added ai.UserContentBlocks
			for _, match := range framedImage.FindAllStringSubmatch(frame, -1) {
				path, media := match[1], match[2]
				if !strings.HasPrefix(path, box.AttachmentFolder+"/") {
					continue
				}
				leftOut := ai.TextContent{Text: fmt.Sprintf("[%s is left out of this request to save space. Read the file to see it.]", path)}
				if images >= imageLimit {
					added = append(added, leftOut)
					continue
				}
				entry, ok := loaded[path]
				if !ok {
					entry = loadImage(path, media, model)
				}
				size := 0
				if entry.image != nil {
					size = len(entry.image.Data)
				}
				if bytes+size > imageBytesLimit {
					added = append(added, leftOut)
					continue
				}
				kept[path] = entry
				if entry.image != nil {
					added = append(added, *entry.image)
					images++
					bytes += size
				}
				if entry.note != "" {
					added = append(added, ai.TextContent{Text: entry.note})
				}
			}
			if len(added) > 0 {
				// A copy, so the session's own message keeps only its frame.
				user := *message.User
				user.Content = slices.Concat(blocks, added)
				messages[i] = agent.AgentMessage{User: &user}
			}
		}
		loaded = kept
		return extension.ContextEventResult{Messages: messages}, nil
	}
}

// userBlocks is the content as blocks, a text being one. A copy of a message changes the blocks, not the message's own.
func userBlocks(content ai.UserContent) ai.UserContentBlocks {
	switch content := content.(type) {
	case ai.UserText:
		return ai.UserContentBlocks{ai.TextContent{Text: string(content)}}
	case ai.UserContentBlocks:
		return content
	}
	return nil
}

// resizeOptions is the options type of PiG's resizeImage.
type resizeOptions = struct {
	MaxWidth    int `json:"maxWidth,omitempty"`
	MaxHeight   int `json:"maxHeight,omitempty"`
	MaxBytes    int `json:"maxBytes,omitempty"`
	JPEGQuality int `json:"jpegQuality,omitempty"`
}

// loadImage is the image of one attachment, shrunk to the model's limit, or the note that takes its place.
func loadImage(path, media string, model *ai.Model) shown {
	// A failed read or resize names the error by its code.
	cannotShow := func(err error) shown {
		reason := err.Error()
		if errno := syscall.Errno(0); errors.As(err, &errno) {
			reason = unix.ErrnoName(errno)
		}
		return shown{note: fmt.Sprintf("[Dyna could not show %s: %s]", path, reason)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cannotShow(err)
	}
	args := subprocess.ResizeImageArgs{Data: base64.StdEncoding.EncodeToString(data), MimeType: media}
	if model != nil && model.InputLimits != nil && model.InputLimits.Images != nil && model.InputLimits.Images.Resize != nil {
		r := model.InputLimits.Images.Resize
		args.Options = &resizeOptions{MaxWidth: r.MaxWidth, MaxHeight: r.MaxHeight, MaxBytes: r.MaxBytes, JPEGQuality: r.JPEGQuality}
	}
	result, err := subprocess.ResizeImage(args)
	if err != nil {
		return cannotShow(err)
	}
	if result.Image == nil {
		return shown{note: fmt.Sprintf("[Dyna could not shrink %s to fit the model. Read the file with your tools.]", path)}
	}
	image := result.Image
	entry := shown{image: &ai.ImageContent{Data: image.Data, MimeType: image.MimeType}}
	// The wire type makes the dimensions optional; a resized image without them gets no note.
	if image.WasResized != nil && *image.WasResized && image.OriginalWidth != nil && image.OriginalHeight != nil && image.Width != nil && image.Height != nil {
		entry.note, _ = sdk.FormatDimensionNote(sdk.ResizedImage{OriginalWidth: float64(*image.OriginalWidth), OriginalHeight: float64(*image.OriginalHeight), Width: float64(*image.Width), Height: float64(*image.Height), WasResized: true})
	}
	return entry
}
