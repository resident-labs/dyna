package computers

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	minDimension = 256
	maxWidth     = 7680
	maxHeight    = 4320
)

func SizeHelp() string {
	return fmt.Sprintf("WIDTHxHEIGHT; width %d–%d pixels, height %d–%d pixels", minDimension, maxWidth, minDimension, maxHeight)
}

// Size is an unscaled desktop output size in pixels.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

func (s Size) Validate() error {
	if s.Width < minDimension || s.Width > maxWidth || s.Height < minDimension || s.Height > maxHeight {
		return fmt.Errorf("size must be %s", SizeHelp())
	}
	return nil
}

func ParseSize(raw string) (Size, error) {
	width, height, ok := strings.Cut(raw, "x")
	w, ew := strconv.Atoi(width)
	h, eh := strconv.Atoi(height)
	if !ok || ew != nil || eh != nil {
		return Size{}, fmt.Errorf("size must use WIDTHxHEIGHT, for example 390x844")
	}
	s := Size{Width: w, Height: h}
	return s, s.Validate()
}
