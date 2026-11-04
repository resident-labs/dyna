package contract

import (
	"regexp"
	"strings"
)

// outsideTag matches the start of an outside tag in any letter case.
var outsideTag = regexp.MustCompile(`(?i)<([\s\x{200b}-\x{200d}\x{feff}]*/?[\s\x{200b}-\x{200d}\x{feff}]*outside)`)

// attributeValue drops what would end or split an attribute.
var attributeValue = strings.NewReplacer("\"", "", "<", "", ">", "", "\n", " ", "\r", " ")

// Outside marks text that reaches an agent from outside the room, with where it came from and who wrote it (decision 13).
// An empty author leaves the attribute out. The tag inside the text is changed, so the text cannot close or fake the block.
// The engine and the box agent share it.
func Outside(source, author, text string) string {
	open := `<outside source="` + attributeValue.Replace(source) + `"`
	if author != "" {
		open += ` author="` + attributeValue.Replace(author) + `"`
	}
	return open + ">\n" + outsideTag.ReplaceAllString(text, "‹${1}") + "\n</outside>"
}
