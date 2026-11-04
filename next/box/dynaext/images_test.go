package dynaext

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

// attachments is a folder of attachments for the test, as box.AttachmentFolder.
func attachments(t *testing.T) string {
	t.Helper()
	folder := t.TempDir()
	previous := box.AttachmentFolder
	box.AttachmentFolder = folder
	t.Cleanup(func() { box.AttachmentFolder = previous })
	return folder
}

// frame writes a PNG attachment of post seq for each image and returns the frame that shows them.
func frame(t *testing.T, folder string, seq int, images ...image.Image) (string, []string) {
	t.Helper()
	var files, paths []string
	for i, picture := range images {
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, picture); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(folder, fmt.Sprint(seq), fmt.Sprintf("pic%d.png", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		files, paths = append(files, path+" (image/png, 1 KB, shown to you)"), append(paths, path)
	}
	return "[Dyna] From the owner, in the room feed. Attached: " + strings.Join(files, ", ") + ".\nLook at this.", paths
}

func blank(side int) image.Image { return image.NewRGBA(image.Rect(0, 0, side, side)) }

// noise is an image PNG cannot compress, of about 4 bytes a pixel.
func noise(side int, seed uint64) image.Image {
	picture := image.NewRGBA(image.Rect(0, 0, side, side))
	random := rand.New(rand.NewPCG(seed, seed))
	for i := range picture.Pix {
		picture.Pix[i] = uint8(random.Uint32())
	}
	picture.Set(0, 0, color.RGBA{A: 255})
	return picture
}

func repeat(n int, picture image.Image) []image.Image {
	images := make([]image.Image, n)
	for i := range images {
		images[i] = picture
	}
	return images
}

// shownImages counts the images in one message as the model received it.
func shownImages(message json.RawMessage) int {
	return strings.Count(string(message), `"type":"image"`)
}

// The model reads each framed image beside the frame, and the session file keeps only the frame.
func TestAFramedImageReachesTheModelAndNotTheSessionFile(t *testing.T) {
	s := newSession(t, []pigtest.Block{{Text: "I see it."}})
	text, _ := frame(t, attachments(t), 7, blank(4))
	s.turn(t, text)
	last := s.model.Requests()[0].Messages[0]
	if shownImages(last) != 1 || !strings.Contains(string(last), `"media_type":"image/png"`) {
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

// A request shows 20 images, the newest first: past that, an image is named instead.
func TestARequestShowsTheNewestTwentyImages(t *testing.T) {
	s := newSession(t, []pigtest.Block{{Text: "ok"}})
	folder := attachments(t)
	older, olderPaths := frame(t, folder, 7, blank(4))
	newer, newerPaths := frame(t, folder, 8, repeat(21, blank(4))...)
	s.turn(t, older, newer)
	messages := s.model.Requests()[0].Messages
	if shownImages(messages[0]) != 0 || !strings.Contains(string(messages[0]), olderPaths[0]+" is left out of this request") {
		t.Fatalf("the older input: %.600s", messages[0])
	}
	if shownImages(messages[1]) != 20 || !strings.Contains(string(messages[1]), newerPaths[20]+" is left out of this request") {
		t.Fatalf("the newer input shows %d images", shownImages(messages[1]))
	}
}

// A request carries at most 20 MiB of images: one that would pass that is named instead.
func TestARequestCarriesTwentyMebibytesOfImages(t *testing.T) {
	s := newSession(t, []pigtest.Block{{Text: "ok"}})
	// Each is about 3.2 MB, or 4.3 MB in base64: four fit in 20 MiB, and five do not fit in 21.
	images := []image.Image{noise(900, 1), noise(900, 2), noise(900, 3), noise(900, 4), noise(900, 5)}
	text, paths := frame(t, attachments(t), 7, images...)
	s.turn(t, text)
	message := s.model.Requests()[0].Messages[0]
	if shownImages(message) != 4 || !strings.Contains(string(message), paths[4]+" is left out of this request") {
		t.Fatalf("%d images shown", shownImages(message))
	}
}

// A big image is shrunk to the model's limit, with a note of its scale.
func TestABigImageIsShrunkWithANote(t *testing.T) {
	s := newSession(t, []pigtest.Block{{Text: "ok"}})
	text, _ := frame(t, attachments(t), 7, blank(3000))
	s.turn(t, text)
	if message := string(s.model.Requests()[0].Messages[0]); !strings.Contains(message, "[Image: original 3000x3000, displayed at 2000x2000") {
		t.Fatalf("the request's input: %.600s", message)
	}
}

// Each request of a turn shows the images it showed before, from memory, so a file removed meanwhile still shows.
func TestALaterRequestShowsTheImagesItHadLoaded(t *testing.T) {
	folder := attachments(t)
	text, paths := frame(t, folder, 7, blank(4))
	command, _ := json.Marshal(map[string]string{"command": "rm " + paths[0]})
	s := newSession(t, []pigtest.Block{{Tool: "bash", Input: string(command)}}, []pigtest.Block{{Text: "ok"}})
	s.turn(t, text)
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("the file is still there: %v", err)
	}
	if got := shownImages(s.model.Requests()[1].Messages[0]); got != 1 {
		t.Fatalf("the second request shows %d images", got)
	}
}

// A file that cannot be read is named with its error code.
func TestAnUnreadableImageIsNamedWithItsError(t *testing.T) {
	s := newSession(t, []pigtest.Block{{Text: "ok"}})
	text, paths := frame(t, attachments(t), 7, blank(4))
	if err := os.Remove(paths[0]); err != nil {
		t.Fatal(err)
	}
	s.turn(t, text)
	if message := string(s.model.Requests()[0].Messages[0]); !strings.Contains(message, "[Dyna could not show "+paths[0]+": ENOENT]") {
		t.Fatalf("the request's input: %.600s", message)
	}
}
