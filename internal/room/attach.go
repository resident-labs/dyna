package room

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"dyna-agent/internal/settings"
)

// Default attachment limits. Effective sizes come from settings.Content.
// Files are stored with the room before the post that names them, so a post
// never points at bytes that are not saved.
const (
	MaxAttachmentBytes = settings.DefaultContentBytes
	MaxPostAttachBytes = 8 * settings.DefaultContentBytes
	MaxPostAttachments = 8
	maxAttachNameBytes = 80
)

// Attachment is one file saved with a room. ID is the first 24 hex digits of
// SHA256, which also names the stored file, so the same bytes are saved once.
type Attachment struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	MIME   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Render "html" asks the desktop to show this file as a sandboxed HTML
	// card. Title and Height come from the document. Post stamps all three
	// from the saved bytes; a sender only asks for Render.
	Render string `json:"render,omitempty"`
	Title  string `json:"title,omitempty"`
	Height int    `json:"height,omitempty"`
	// Net marks a card whose sender allowed https loads (dyna post
	// --allow-net). Without it the card's CSP blocks the network.
	Net bool `json:"net,omitempty"`
	// Visual is the ID of the post that created the visualization this
	// card updates; it is empty on the first version. Version counts from
	// 1 and is stamped by Post.
	Visual  string `json:"visual,omitempty"`
	Version int    `json:"version,omitempty"`
	// The preview the posting agent rendered: banner and thumbnail images,
	// one screenshot per checked width, and the quality checks. Images are
	// attachments saved in the same room. All are empty when no browser
	// could render the card.
	Banner string  `json:"banner,omitempty"`
	Thumb  string  `json:"thumb,omitempty"`
	Shots  []Shot  `json:"shots,omitempty"`
	Checks []Check `json:"checks,omitempty"`
}

// Image reports an attachment a viewer can show inline.
func (a Attachment) Image() bool {
	switch a.MIME {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// File is the stored file name: the ID plus an extension from the content
// class, so file tools that go by extension (image viewers) open it right.
func (a Attachment) File() string { return a.ID + extFor(a.MIME) }

func extFor(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	if strings.HasPrefix(mime, "text/") {
		return ".txt"
	}
	return ".bin"
}

// Text reports an attachment that is readable as UTF-8 text.
func (a Attachment) Text() bool { return strings.HasPrefix(a.MIME, "text/") }

var (
	attachIDRe   = regexp.MustCompile(`^[0-9a-f]{24}$`)
	unsafeNameRe = regexp.MustCompile(`[\x00-\x1f/\\]+`)
)

// ValidAttachmentID reports a well-formed attachment ID.
func ValidAttachmentID(id string) bool { return attachIDRe.MatchString(id) }

// FilesDir is where a room keeps its attachment bytes.
func (s *Store) FilesDir(roomID string) (string, error) {
	dir, err := s.dir(roomID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "files"), nil
}

// AttachmentPath is the absolute path of a saved attachment's bytes.
func (s *Store) AttachmentPath(roomID, id string) (string, error) {
	if !ValidAttachmentID(id) {
		return "", fmt.Errorf("invalid attachment id %q", id)
	}
	dir, err := s.FilesDir(roomID)
	if err != nil {
		return "", err
	}
	matches, err := filepath.Glob(filepath.Join(dir, id+".*"))
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("attachment %s is not saved in this room", id)
	}
	return matches[0], nil
}

// SanitizeAttachmentName keeps a file name readable and safe to show: no
// path parts, no control characters, at most a short length.
func SanitizeAttachmentName(name string) string {
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	name = strings.TrimSpace(unsafeNameRe.ReplaceAllString(name, "_"))
	if !utf8.ValidString(name) {
		name = strings.ToValidUTF8(name, "_")
	}
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	if len(name) > maxAttachNameBytes {
		ext := filepath.Ext(name)
		if len(ext) > 12 {
			ext = ""
		}
		name = TruncateUTF8(name, maxAttachNameBytes-len(ext)-1) + "…" + ext
	}
	return name
}

// sniffMIME names the content type from the bytes first and the file name
// second. Only the four inline image types count as images.
func sniffMIME(name string, data []byte) string {
	mime := http.DetectContentType(data)
	if i := strings.Index(mime, ";"); i >= 0 {
		mime = mime[:i]
	}
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return mime
	}
	if utf8.Valid(data) && !strings.ContainsRune(string(data), 0) {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".md", ".markdown":
			return "text/markdown"
		case ".json":
			return "text/json"
		case ".csv":
			return "text/csv"
		}
		return "text/plain"
	}
	if mime == "application/octet-stream" || strings.HasPrefix(mime, "text/") {
		return "application/octet-stream"
	}
	return mime
}

// PutAttachment saves bytes with a room and returns their record. Saving the
// same bytes again returns the same ID. The write is atomic: a reader never
// sees a partial file.
func (s *Store) PutAttachment(roomID, name string, data []byte) (Attachment, error) {
	return s.PutRenderedAttachment(roomID, name, data, "")
}

// PutRenderedAttachment uses the independent card limit for marked cards.
func (s *Store) PutRenderedAttachment(roomID, name string, data []byte, render string) (Attachment, error) {
	limits, err := settings.Content()
	if err != nil {
		return Attachment{}, err
	}
	limit := limits.FileBytes
	if render == RenderHTML {
		limit = limits.CardBytes
	}
	if len(data) == 0 {
		return Attachment{}, errors.New("the file is empty")
	}
	if int64(len(data)) > limit {
		return Attachment{}, fmt.Errorf("the file is too large (maximum %s)", settings.FormatBytes(int64(limit)))
	}
	if _, err := s.readRecord(roomID); err != nil {
		return Attachment{}, err
	}
	sum := sha256.Sum256(data)
	full := hex.EncodeToString(sum[:])
	att := Attachment{ID: full[:24], Name: SanitizeAttachmentName(name), MIME: sniffMIME(name, data), Size: int64(len(data)), SHA256: full}
	dir, err := s.FilesDir(roomID)
	if err != nil {
		return Attachment{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Attachment{}, err
	}
	if path, err := s.AttachmentPath(roomID, att.ID); err == nil {
		if st, err := os.Stat(path); err == nil && st.Size() == att.Size {
			return att, nil
		}
	}
	path := filepath.Join(dir, att.File())
	tmp, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return Attachment{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return Attachment{}, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return Attachment{}, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return Attachment{}, err
	}
	return att, nil
}

// canonAttachments rebuilds each attachment record from its saved bytes, so
// a post can name only files saved in its room and cannot misstate their
// type, size or digest. The caller's name is kept, cleaned.
func (s *Store) canonAttachments(roomID string, atts []Attachment) ([]Attachment, error) {
	if len(atts) > MaxPostAttachments {
		return nil, fmt.Errorf("a post can carry at most %d attachments", MaxPostAttachments)
	}
	limits, err := settings.Content()
	if err != nil {
		return nil, err
	}
	var out []Attachment
	var total int64
	rich := 0
	for _, a := range atts {
		path, err := s.AttachmentPath(roomID, a.ID)
		if err != nil {
			return nil, err
		}
		limit := limits.FileBytes
		if a.Rich() {
			limit = limits.CardBytes
		}
		data, err := settings.ReadContentFile(path, limit)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		full := hex.EncodeToString(sum[:])
		if full[:24] != a.ID {
			return nil, fmt.Errorf("attachment %s does not match its saved bytes", a.ID)
		}
		name := SanitizeAttachmentName(a.Name)
		c := Attachment{ID: a.ID, Name: name, MIME: sniffMIME(name, data), Size: int64(len(data)), SHA256: full}
		switch a.Render {
		case "":
			if a.Visual != "" {
				return nil, errors.New("only an HTML card can update a visual")
			}
			if a.Banner != "" || a.Thumb != "" || len(a.Shots) > 0 || len(a.Checks) > 0 {
				return nil, errors.New("only an HTML card has a preview")
			}
		case RenderHTML:
			if rich++; rich > 1 {
				return nil, errors.New("a post can carry at most one rich HTML attachment")
			}
			meta, err := ParseRich(name, data)
			if err != nil {
				return nil, err
			}
			if err := s.checkPreview(roomID, a); err != nil {
				return nil, err
			}
			if meta.Title == "" {
				meta.Title = cleanTitle(strings.TrimSuffix(name, filepath.Ext(name)))
			}
			c.MIME, c.Render, c.Title, c.Height, c.Net, c.Visual = "text/html", RenderHTML, meta.Title, meta.Height, a.Net, a.Visual
			c.Banner, c.Thumb, c.Shots, c.Checks = a.Banner, a.Thumb, a.Shots, a.Checks
		default:
			return nil, fmt.Errorf("unknown attachment render %q", a.Render)
		}
		out = append(out, c)
		total += int64(len(data))
	}
	if total > limits.PostBytes() {
		return nil, fmt.Errorf("attachments are too large together (maximum %s)", settings.FormatBytes(int64(limits.PostBytes())))
	}
	return out, nil
}

// HumanSize renders a byte count as 512 B, 3.4 KB or 2.1 MB.
func HumanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// manifest renders the attachment list a recipient reads. Each line names
// the file and where its bytes are. limit > 0 bounds the bytes; lines that do
// not fit are counted and point to the full post.
func (m Message) manifest(o FrameOptions, limit int) string {
	if len(m.Attachments) == 0 {
		return ""
	}
	var b strings.Builder
	head := fmt.Sprintf("Attachments (%d), saved with the room:", len(m.Attachments))
	tail := m.attachHint(o)
	b.WriteString(head)
	moreLine := func(n int) string { return fmt.Sprintf("\n- +%d more: dyna room read %s %s", n, o.Room, m.ID) }
	for i, a := range m.Attachments {
		where := "dyna attachment get " + o.Room + " " + a.ID
		if o.FilesDir != "" {
			where = filepath.Join(o.FilesDir, a.File())
		}
		kind := a.MIME
		if a.Rich() {
			kind = fmt.Sprintf("HTML card %q, visual %s v%d", a.RichLabel(), a.VisualID(m.ID), a.Version)
		}
		line := fmt.Sprintf("\n- %s · %s · %s · %s", a.Name, kind, HumanSize(a.Size), where)
		reserve := 0
		if i < len(m.Attachments)-1 {
			reserve = len(moreLine(len(m.Attachments) - i - 1))
		}
		if limit > 0 && b.Len()+len(line)+reserve+len(tail) > limit {
			b.WriteString(moreLine(len(m.Attachments) - i))
			break
		}
		b.WriteString(line)
	}
	b.WriteString(tail)
	return b.String()
}

// attachHint says how to open the files, honestly for images.
func (m Message) attachHint(o FrameOptions) string {
	images, rich := false, false
	for _, a := range m.Attachments {
		images = images || a.Image()
		rich = rich || a.Rich()
	}
	hint := "\nOpen each file with your file-reading tool."
	if images {
		hint += " To see an image you need a tool that reads image files; if you have none, say so instead of guessing what it shows."
	}
	if rich {
		hint += " An HTML card renders only in the Dyna desktop app; its file is the HTML source."
	}
	if o.FilesDir != "" {
		hint += fmt.Sprintf("\nIf a path does not open: dyna attachment get %s <id> prints it, and -o - writes the bytes.", o.Room)
	}
	return hint
}
