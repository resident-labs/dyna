package visual

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Meta reads untrusted HTML once, before it is uploaded.
func Meta(name string, data []byte) (string, int, error) {
	ext := strings.ToLower(filepath.Ext(name))
	if ext != ".html" && ext != ".htm" {
		return "", 0, fmt.Errorf("visual must be an .html or .htm file")
	}
	if len(data) > 25<<20 {
		return "", 0, fmt.Errorf("visual exceeds 25 MiB")
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", 0, fmt.Errorf("visual must be UTF-8 text")
	}
	height := 360
	var title strings.Builder
	inTitle, titled, sized := false, false, false
	z := html.NewTokenizer(bytes.NewReader(data))
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			if z.Err() != io.EOF {
				return "", 0, z.Err()
			}
			break
		}
		t := z.Token()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			switch t.Data {
			case "base":
				return "", 0, fmt.Errorf("visual cannot contain a base element")
			case "title":
				inTitle = !titled
			case "meta":
				var name, content, equiv string
				for _, a := range t.Attr {
					switch a.Key {
					case "name":
						name = a.Val
					case "content":
						content = a.Val
					case "http-equiv":
						equiv = a.Val
					}
				}
				if strings.EqualFold(strings.TrimSpace(equiv), "refresh") {
					return "", 0, fmt.Errorf("visual cannot refresh")
				}
				if strings.EqualFold(name, "dyna-height") && !sized {
					sized = true
					if h, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(content, "px"))); err == nil {
						height = min(max(h, 120), 1200)
					}
				}
			}
		case html.EndTagToken:
			if t.Data == "title" && inTitle {
				inTitle, titled = false, true
			}
		case html.TextToken:
			if inTitle {
				title.WriteString(t.Data)
			}
		}
	}
	label := strings.Join(strings.Fields(title.String()), " ")
	if label == "" {
		label = filepath.Base(name)
	}
	if len(label) > 255 {
		label = label[:255]
		for !utf8.ValidString(label) {
			label = label[:len(label)-1]
		}
	}
	return label, height, nil
}
