package pi

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptImageStaysOffTheWire(t *testing.T) {
	result := func(id, media string) string {
		return `{"type":"message","message":{"role":"toolResult","toolCallId":"` + id + `","content":[{"type":"text","text":"Read image file [` + media + `]"},{"type":"image","data":"aGVsbG8=","mimeType":"` + media + `"}]}}` + "\n"
	}
	data := `{"type":"message","message":{"role":"user","deliveryId":"1"}}` + "\n" +
		`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","id":"img","name":"read","arguments":{"path":"/w/a.png"}},{"type":"toolCall","id":"odd","name":"read","arguments":{"path":"/w/b.png"}},{"type":"toolCall","id":"txt","name":"read","arguments":{"path":"/w/c.txt"}},{"type":"toolCall","id":"bash","name":"bash","arguments":{"command":"x"}}]}}` + "\n" +
		result("img", "image/png") + result("odd", "text/html") +
		`{"type":"message","message":{"role":"toolResult","toolCallId":"txt","content":[{"type":"text","text":"hello"}]}}` + "\n" +
		result("bash", "image/png")
	deliveries := map[contract.DeliverySeq]contract.TurnID{1: "t"}
	items, _, _, err := ReadTranscript(strings.NewReader(data), deliveries, "x", "", 0, 10)
	if err != nil || len(items) != 4 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	image := items[0].Image
	if image == nil || image.Item != "img" || image.Media != "image/png" || image.Bytes != 6 || items[0].ImageData != "" {
		t.Fatalf("image=%+v data=%q", image, items[0].ImageData)
	}
	if strings.Contains(items[0].Output, "aGVsbG8") {
		t.Fatalf("output carries the bytes: %q", items[0].Output)
	}
	for _, item := range items[1:] {
		if item.Image != nil {
			t.Fatalf("%s has an image: %+v", item.ID, item.Image)
		}
	}
	if step := TranscriptStep(items[0].TranscriptItem); step.Image == nil || step.Image.Item != "img" {
		t.Fatalf("step image=%+v", step.Image)
	}
	full, _, _, err := ReadTranscript(strings.NewReader(data), deliveries, "x", "img", 0, 1)
	if err != nil || len(full) != 1 || full[0].ImageData != "aGVsbG8=" {
		t.Fatalf("full=%+v err=%v", full, err)
	}
}
