package ghclient

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"path/filepath"
	"strings"
)

const maxMediaSize = 25 << 20
const maxMediaPixels = 40_000_000

// sanitizeMedia removes image metadata by decoding and re-encoding pixels.
// Animated GIFs retain their frames, timing, disposal and loop count.
func sanitizeMedia(path string, data []byte) ([]byte, error) {
	if len(data) > maxMediaSize {
		return nil, fmt.Errorf("media exceeds 25 MiB")
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".mp4":
		if len(data) < 12 || string(data[4:8]) != "ftyp" || binary.BigEndian.Uint32(data[:4]) < 12 || uint64(binary.BigEndian.Uint32(data[:4])) > uint64(len(data)) {
			return nil, fmt.Errorf("invalid MP4 signature")
		}
		return data, nil
	case ".webm":
		if len(data) < 4 || !bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}) {
			return nil, fmt.Errorf("invalid WebM signature")
		}
		return data, nil
	case ".webp":
		return nil, fmt.Errorf("WebP cannot be safely sanitized; allowed formats: PNG, JPEG, GIF, MP4, WebM")
	case ".png", ".jpg", ".jpeg", ".gif":
	default:
		return nil, fmt.Errorf("unsupported media type; allowed formats: PNG, JPEG, GIF, MP4, WebM")
	}
	var cfg image.Config
	var err error
	switch ext {
	case ".png":
		if len(data) < 8 || !bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")) {
			return nil, fmt.Errorf("invalid PNG signature")
		}
		// The standard decoder silently ignores APNG frames, so reject animation.
		for p := 8; p+12 <= len(data); {
			n := uint64(binary.BigEndian.Uint32(data[p : p+4]))
			if n > uint64(len(data)-p-12) {
				return nil, fmt.Errorf("invalid PNG chunk")
			}
			if string(data[p+4:p+8]) == "acTL" {
				return nil, fmt.Errorf("animated PNG cannot be safely sanitized")
			}
			p += int(n) + 12
		}
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case ".jpg", ".jpeg":
		if len(data) < 3 || !bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}) {
			return nil, fmt.Errorf("invalid JPEG signature")
		}
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case ".gif":
		if len(data) < 6 || (string(data[:6]) != "GIF87a" && string(data[:6]) != "GIF89a") {
			return nil, fmt.Errorf("invalid GIF signature")
		}
		if err = checkGIFBudget(data); err != nil {
			return nil, err
		}
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	}
	if err != nil {
		return nil, fmt.Errorf("invalid image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxMediaPixels {
		return nil, fmt.Errorf("image dimensions exceed limit")
	}
	var out bytes.Buffer
	switch ext {
	case ".png":
		var img image.Image
		img, err = png.Decode(bytes.NewReader(data))
		if err == nil {
			err = png.Encode(&out, img)
		}
	case ".jpg", ".jpeg":
		var img image.Image
		img, err = jpeg.Decode(bytes.NewReader(data))
		if err == nil {
			err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 95})
		}
	case ".gif":
		var img *gif.GIF
		img, err = gif.DecodeAll(bytes.NewReader(data))
		if err == nil {
			err = gif.EncodeAll(&out, img)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("invalid image: %w", err)
	}
	if out.Len() > maxMediaSize {
		return nil, fmt.Errorf("sanitized media exceeds 25 MiB")
	}
	return out.Bytes(), nil
}

// checkGIFBudget bounds the total decoded frame area before DecodeAll allocates.
func checkGIFBudget(data []byte) error {
	bad := fmt.Errorf("invalid or oversized GIF")
	if len(data) < 13 {
		return bad
	}
	p := 13
	if data[10]&0x80 != 0 {
		p += 3 << (uint(data[10]&7) + 1)
	}
	var pixels int64
	for p < len(data) {
		marker := data[p]
		p++
		switch marker {
		case 0x3b:
			return nil
		case 0x2c:
			if p+9 > len(data) {
				return bad
			}
			w, h := binary.LittleEndian.Uint16(data[p+4:p+6]), binary.LittleEndian.Uint16(data[p+6:p+8])
			pixels += int64(w) * int64(h)
			if w == 0 || h == 0 || pixels > maxMediaPixels {
				return bad
			}
			flags := data[p+8]
			p += 9
			if flags&0x80 != 0 {
				p += 3 << (uint(flags&7) + 1)
			}
			if p >= len(data) {
				return bad
			}
			p++ // LZW code size
		case 0x21:
			if p >= len(data) {
				return bad
			}
			p++ // extension label
		default:
			return bad
		}
		for {
			if p >= len(data) {
				return bad
			}
			n := int(data[p])
			p++
			if n == 0 {
				break
			}
			if n > len(data)-p {
				return bad
			}
			p += n
		}
	}
	return bad
}

// SanitizeMedia validates and removes image metadata at the engine trust boundary.
func SanitizeMedia(path string, data []byte) ([]byte, error) { return sanitizeMedia(path, data) }
