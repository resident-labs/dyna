package room

import (
	"strings"
	"unicode/utf16"
)

// Split respects Discord's UTF-16 limit and preserves fenced blocks across messages.
func SplitDiscord(text string) []string {
	var chunks []string
	fence := ""
	prefix := ""
	for text != "" {
		capacity := 2000 - len(utf16.Encode([]rune(prefix))) - 8
		runes := []rune(text)
		units, end, lastLine := 0, 0, 0
		for end < len(runes) {
			n := 1
			if runes[end] > 0xffff {
				n = 2
			}
			if units+n > capacity {
				break
			}
			units += n
			end++
			if runes[end-1] == '\n' {
				lastLine = end
			}
		}
		if end < len(runes) && lastLine > 0 {
			end = lastLine
		}
		piece := string(runes[:end])
		text = string(runes[end:])
		for _, line := range strings.Split(piece, "\n") {
			if strings.HasPrefix(line, "```") {
				if fence != "" {
					fence = ""
				} else {
					fence = strings.TrimSpace(strings.TrimPrefix(line, "```"))
					if len([]rune(fence)) > 32 {
						fence = " "
					}
					if fence == "" {
						fence = " "
					}
				}
			}
		}
		chunk := prefix + piece
		prefix = ""
		if fence != "" && text != "" {
			chunk += "\n```"
			prefix = "```" + strings.TrimSpace(fence) + "\n"
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}
