package agy

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/resident-labs/dyna/contract"
)

// Attachment limits are T3's (ACPS:336-539; PROVIDER_SEND_TURN_MAX_IMAGE_BYTES and _FILE_BYTES).
const (
	maxImageBytes = 10 << 20
	maxAudioBytes = 20 << 20
	maxTextBytes  = 1 << 20
	maxTotalBytes = 50 << 20
)

var (
	imageTypes = map[string]bool{"image/bmp": true, "image/jpeg": true, "image/png": true, "image/webp": true}
	audioTypes = map[string]bool{"audio/aac": true, "audio/flac": true, "audio/mp3": true, "audio/mpeg": true, "audio/mp4": true, "audio/m4a": true,
		"audio/x-m4a": true, "audio/ogg": true, "audio/wav": true, "audio/x-wav": true, "audio/webm": true}
	textTypes = map[string]bool{"application/json": true, "application/ld+json": true, "application/javascript": true, "application/typescript": true,
		"application/xml": true, "application/yaml": true, "application/x-yaml": true, "application/x-sh": true}
	textExtensions = map[string]bool{".txt": true, ".md": true, ".mdx": true, ".json": true, ".jsonl": true, ".yaml": true, ".yml": true, ".toml": true,
		".xml": true, ".csv": true, ".tsv": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".html": true,
		".css": true, ".scss": true, ".less": true, ".py": true, ".rs": true, ".go": true, ".java": true, ".kt": true, ".swift": true, ".c": true,
		".h": true, ".cc": true, ".cpp": true, ".hpp": true, ".cs": true, ".rb": true, ".php": true, ".sh": true, ".bash": true, ".zsh": true,
		".sql": true, ".graphql": true, ".svelte": true, ".vue": true, ".log": true, ".diff": true, ".patch": true, ".ini": true, ".conf": true}
	// extra covers types the system's mime table may lack.
	extraTypes = map[string]string{".bmp": "image/bmp", ".webp": "image/webp", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
		".pdf": "application/pdf", ".m4a": "audio/m4a", ".flac": "audio/flac", ".wav": "audio/wav", ".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".aac": "audio/aac"}
)

// attachmentBlocks is T3's buildAntigravityPrompt for an input's files (ACPS:413-539). Images and audio go as base64 when
// agy advertises them, a PDF as a resource link and text as an embedded resource when it advertises embedded context; any
// other file, and any too large for its limit, stays path-only: the input's text already names its path. An image over its
// limit fails the turn, as in T3. total is the bytes of native attachments already in the prompt. Dyna differs from T3 in one
// way: a file that is not in the box (the frame says it could not be fetched) is skipped, where T3 fails the turn.
func attachmentBlocks(files []contract.TurnFile, p *process, total *int64) ([]map[string]any, error) {
	var blocks []map[string]any
	for _, f := range files {
		name := filepath.Base(f.Path)
		ext := strings.ToLower(filepath.Ext(name))
		mimeType := extraTypes[ext]
		if mimeType == "" {
			mimeType, _, _ = strings.Cut(mime.TypeByExtension(ext), ";")
		}
		image, audio, pdf := imageTypes[mimeType], audioTypes[mimeType], mimeType == "application/pdf"
		text := strings.HasPrefix(mimeType, "text/") || textTypes[mimeType] || textExtensions[ext]
		pathOnly := (image && !p.image) || (audio && !p.audio) || (!image && !audio && (!(pdf || text) || !p.embedded))
		if pathOnly {
			continue
		}
		info, err := os.Stat(f.Path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		limit := int64(maxTextBytes)
		switch {
		case image:
			limit = maxImageBytes
		case audio:
			limit = maxAudioBytes
		case pdf:
			limit = maxTotalBytes
		}
		size := info.Size()
		if size > limit || *total+size > maxTotalBytes {
			if image {
				return nil, fmt.Errorf("image %q is too large: agy accepts images up to 10 MiB and 50 MiB of native attachments per message", name)
			}
			continue
		}
		uri := "file://" + filepath.ToSlash(f.Path)
		if pdf {
			*total += size
			blocks = append(blocks, map[string]any{"type": "resource_link", "uri": uri, "name": name, "mimeType": mimeType, "size": size})
			continue
		}
		data, err := os.ReadFile(f.Path)
		if err != nil || int64(len(data)) > limit || *total+int64(len(data)) > maxTotalBytes {
			continue
		}
		switch {
		case image:
			blocks = append(blocks, map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mimeType})
		case audio:
			blocks = append(blocks, map[string]any{"type": "audio", "data": base64.StdEncoding.EncodeToString(data), "mimeType": mimeType})
		default:
			decoded, ok := decodeText(data)
			if !ok {
				continue // other encodings and binary data stay path-only
			}
			if mimeType == "" {
				mimeType = "text/plain"
			}
			blocks = append(blocks, map[string]any{"type": "resource", "resource": map[string]any{"uri": uri, "mimeType": mimeType, "text": decoded}})
		}
		*total += int64(len(data))
	}
	return blocks, nil
}

// decodeText reads UTF-8, or UTF-16 with a byte order mark; a NUL byte or invalid text is binary.
func decodeText(data []byte) (string, bool) {
	var s string
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		order := binary.ByteOrder(binary.LittleEndian)
		if data[0] == 0xfe {
			order = binary.BigEndian
		}
		body := data[2:]
		if len(body)%2 != 0 {
			return "", false
		}
		units := make([]uint16, len(body)/2)
		for i := range units {
			units[i] = order.Uint16(body[2*i:])
		}
		s = string(utf16.Decode(units))
	case utf8.Valid(data):
		s = string(data)
	default:
		return "", false
	}
	return s, !strings.ContainsRune(s, 0)
}
