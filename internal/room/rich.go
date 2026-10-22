package room

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"dyna-agent/internal/settings"
)

// Rich HTML attachments. A sender marks one attachment of a post to render
// as an HTML card in the desktop app; every other reader sees the post text
// and a normal manifest line. The desktop runs card scripts in a sandboxed,
// opaque-origin frame, and only behind its IPC gate: in WebKitGTK every
// frame can reach the page's Wails message handler.
const (
	// RenderHTML is the only render mark.
	RenderHTML = "html"
	// MaxRichBytes is the default card limit; settings.Content is authoritative.
	MaxRichBytes = settings.DefaultContentBytes
	// Card heights in CSS pixels. A document can ask for its own height
	// with <meta name="dyna-height" content="480">. Clients clamp again.
	RichHeightDefault = 360
	RichHeightMin     = 120
	RichHeightMax     = 1200
	maxRichTitleBytes = 80
)

// Rich reports an attachment the desktop renders as an HTML card.
func (a Attachment) Rich() bool { return a.Render == RenderHTML }

// RichMeta is what Go reads from a rich document. Height 0 means the
// document did not ask; clients use RichHeightDefault. The client never parses
// agent HTML to learn these.
type RichMeta struct {
	Title  string
	Height int
}

// ParseRich checks that data can render as a rich card and reads its title
// and requested height. name must end in .html or .htm.
func ParseRich(name string, data []byte) (RichMeta, error) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html", ".htm":
	default:
		return RichMeta{}, fmt.Errorf("a rich attachment must be an .html file, not %q", name)
	}
	limits, err := settings.Content()
	if err != nil {
		return RichMeta{}, err
	}
	if int64(len(data)) > limits.CardBytes {
		return RichMeta{}, fmt.Errorf("the HTML file is too large (maximum %s)", settings.FormatBytes(int64(limits.CardBytes)))
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return RichMeta{}, errors.New("the HTML file must be UTF-8 text")
	}
	var meta RichMeta
	sized := false
	// The tokenizer reads tags and attributes the way a browser does: it
	// lowercases names and decodes entities, so "&#114;efresh" is caught.
	z := html.NewTokenizer(bytes.NewReader(data))
	// The card's name is its <title>, else its first <h1>.
	inTitle, titled := false, false
	inH1, headed := false, false
	var title, h1 strings.Builder
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() == io.EOF {
				break
			}
			return RichMeta{}, z.Err()
		}
		tok := z.Token()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			switch tok.Data {
			case "meta":
				if attr(tok, "http-equiv") != "" && strings.EqualFold(strings.TrimSpace(attr(tok, "http-equiv")), "refresh") {
					// A refresh navigates the card with no click and no script.
					return RichMeta{}, errors.New(`the HTML file uses <meta http-equiv="refresh">, which cards do not allow`)
				}
				if strings.EqualFold(attr(tok, "name"), "dyna-height") && !sized {
					// The first one wins. A bad value leaves 0, the client default.
					sized = true
					if h, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(attr(tok, "content"), "px"))); err == nil {
						meta.Height = min(max(h, RichHeightMin), RichHeightMax)
					}
				}
			case "base":
				// A self-contained card has no use for <base>; it only moves
				// link targets.
				return RichMeta{}, errors.New("the HTML file uses <base>, which cards do not allow")
			case "title":
				inTitle = !titled
			case "h1":
				inH1 = !headed
			}
		case html.EndTagToken:
			if tok.Data == "title" && inTitle {
				inTitle, titled = false, true
			}
			if tok.Data == "h1" && inH1 {
				inH1, headed = false, true
			}
		case html.TextToken:
			if inTitle {
				title.WriteString(tok.Data)
			}
			if inH1 {
				h1.WriteString(tok.Data)
			}
		}
	}
	meta.Title = cleanTitle(title.String())
	if meta.Title == "" {
		meta.Title = cleanTitle(h1.String())
	}
	return meta, nil
}

func attr(t html.Token, key string) string {
	for _, a := range t.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// cleanTitle keeps a title short, on one line and free of control
// characters. Clients show it as text, never as HTML.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxRichTitleBytes {
		s = TruncateUTF8(s, maxRichTitleBytes-len("…")) + "…"
	}
	return s
}

// CardHeight is the height a client draws: the stored height, clamped, or
// the default when the document did not ask.
func (a Attachment) CardHeight() int {
	if a.Height == 0 {
		return RichHeightDefault
	}
	return min(max(a.Height, RichHeightMin), RichHeightMax)
}

// RichLabel is the card's name in text views: its title, else its file name.
func (a Attachment) RichLabel() string {
	if a.Title != "" {
		return a.Title
	}
	return a.Name
}
