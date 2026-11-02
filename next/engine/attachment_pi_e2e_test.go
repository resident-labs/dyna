//go:build linux

package engine

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"testing"
)

func noisePNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetNRGBA(x, y, color.NRGBA{uint8(rand.IntN(256)), uint8(rand.IntN(256)), uint8(rand.IntN(256)), 255})
		}
	}
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// anthropicRequest is what a request body holds in image blocks and text.
type anthropicRequest struct {
	images []struct{ media, data string }
	text   string
}

func readAnthropicRequest(t *testing.T, body string) anthropicRequest {
	t.Helper()
	var request struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	var out anthropicRequest
	for _, message := range request.Messages {
		var blocks []struct {
			Type, Text string
			Source     struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			}
		}
		if json.Unmarshal(message.Content, &blocks) != nil {
			out.text += string(message.Content) + "\n"
			continue
		}
		for _, block := range blocks {
			if block.Type == "image" {
				out.images = append(out.images, struct{ media, data string }{block.Source.MediaType, block.Source.Data})
			}
			out.text += block.Text + "\n"
		}
	}
	return out
}
