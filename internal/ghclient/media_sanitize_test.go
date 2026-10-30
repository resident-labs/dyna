package ghclient

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"testing"
)

func TestSanitizeMedia(t *testing.T) {
	var src bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(&src, img); err != nil {
		t.Fatal(err)
	}
	// Insert a valid text chunk before IEND; re-encoding must remove it.
	data := src.Bytes()
	chunk := append([]byte("tEXt"), []byte("key\x00private")...)
	var metadata bytes.Buffer
	_ = binary.Write(&metadata, binary.BigEndian, uint32(len(chunk)-4))
	metadata.Write(chunk)
	_ = binary.Write(&metadata, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	data = append(append(append([]byte{}, data[:len(data)-12]...), metadata.Bytes()...), data[len(data)-12:]...)
	out, err := sanitizeMedia("image.png", data)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("private")) {
		t.Fatal("metadata retained")
	}
	if _, err = png.Decode(bytes.NewReader(out)); err != nil {
		t.Fatal(err)
	}
	if _, err = sanitizeMedia("image.jpg", data); err == nil {
		t.Fatal("accepted mismatched extension")
	}
	if _, err = sanitizeMedia("image.png", data[:len(data)/2]); err == nil {
		t.Fatal("accepted truncated PNG")
	}
	if _, err = sanitizeMedia("image.webp", []byte("RIFF")); err == nil {
		t.Fatal("accepted WebP")
	}
}

func TestSanitizeGIFAnimation(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	a := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	b := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	b.SetColorIndex(0, 0, 1)
	var src bytes.Buffer
	if err := gif.EncodeAll(&src, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{2, 3}, LoopCount: 4}); err != nil {
		t.Fatal(err)
	}
	out, err := sanitizeMedia("animation.gif", src.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	got, err := gif.DecodeAll(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Image) != 2 || got.Delay[1] != 3 || got.LoopCount != 4 || got.Image[1].ColorIndexAt(0, 0) != 1 {
		t.Fatal("animation changed")
	}
}
