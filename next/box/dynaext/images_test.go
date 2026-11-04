package dynaext

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// attach writes PNG attachments of the given sides and returns the frame that shows them.
func attach(t *testing.T, sides ...int) string {
	t.Helper()
	folder := t.TempDir()
	previous := box.AttachmentFolder
	box.AttachmentFolder = folder
	t.Cleanup(func() { box.AttachmentFolder = previous })
	var files []string
	for i, side := range sides {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, side, side))); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, "7", fmt.Sprintf("pic%d.png", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, path+" (image/png, 1 KB, shown to you)")
	}
	return "[Dyna] From the owner, in the room feed. Attached: " + strings.Join(files, ", ") + ".\nLook at this."
}

// The model reads each framed image beside the frame, and the session file keeps only the frame.
func TestAFramedImageReachesTheModelAndNotTheSessionFile(t *testing.T) {
	s := newSession(t, []pigtest.Block{{Text: "I see it."}})
	s.turn(t, attach(t, 4))
	requests := s.model.Requests()
	last := string(requests[0].Messages[len(requests[0].Messages)-1])
	if !strings.Contains(last, `"type":"image"`) || !strings.Contains(last, `"media_type":"image/png"`) {
		t.Fatalf("the request's input: %s", last)
	}
	data, err := os.ReadFile(s.harness.SessionFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"type":"image"`) {
		t.Fatal("the session file holds the image")
	}
}

// Past the limit, an image is named instead of shown, and a big one is shrunk with a note of its scale.
func TestImagesPastTheLimitAreNamedAndBigOnesShrunk(t *testing.T) {
	s := newSession(t, []pigtest.Block{{Text: "ok"}})
	sides := []int{3000}
	for len(sides) <= imageLimit {
		sides = append(sides, 4)
	}
	s.turn(t, attach(t, sides...))
	last := string(s.model.Requests()[0].Messages[0])
	if got := strings.Count(last, `"type":"image"`); got != imageLimit {
		t.Fatalf("%d images shown", got)
	}
	if !strings.Contains(last, "is left out of this request to save space") || !strings.Contains(last, "[Image: original 3000x3000, displayed at 2000x2000") {
		t.Fatalf("the request's input: %.2000s", last)
	}
}
