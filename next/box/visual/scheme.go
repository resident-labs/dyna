package visual

import (
	"bytes"
	"strings"

	"golang.org/x/net/html"
)

type Mode string

const (
	Default Mode = ""
	Light   Mode = "light"
	Dark    Mode = "dark"
)

// ColorScheme supplies a default only when the document has no color-scheme meta.
func ColorScheme(doc []byte, mode Mode) []byte {
	z := html.NewTokenizer(bytes.NewReader(doc))
	offset, insert := 0, 0
	head := false
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			break
		}
		raw := z.Raw()
		offset += len(raw)
		if kind == html.DoctypeToken && !head {
			insert = offset
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		t := z.Token()
		if t.Data == "head" {
			head = true
			insert = offset
		}
		if t.Data == "meta" {
			for _, a := range t.Attr {
				if a.Key == "name" && strings.EqualFold(strings.TrimSpace(a.Val), "color-scheme") {
					return doc
				}
			}
		}
	}
	meta := `<meta name="color-scheme" content="light dark">`
	if mode != Default {
		meta += `<style>html{color-scheme:` + string(mode) + `;background:transparent}</style>`
	}
	out := make([]byte, 0, len(doc)+len(meta))
	out = append(out, doc[:insert]...)
	out = append(out, meta...)
	out = append(out, doc[insert:]...)
	return out
}
