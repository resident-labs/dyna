package discord

import "strings"

type Attachment struct {
	URL  string `json:"url"`
	Name string `json:"filename"`
	Size int64  `json:"size"`
}

// Files are the message's attachments, then those of a message it forwards.
func (m Message) Files() []Attachment {
	files := m.Attachments
	for _, s := range m.Snapshots {
		files = append(files[:len(files):len(files)], s.Message.Attachments...)
	}
	return files
}

// Parts names what the message carries besides its text and files, one line each, so a reader sees that an embed, a
// sticker, a poll or a forwarded message is there.
func (m Message) Parts() []string {
	var parts []string
	for _, s := range m.Snapshots {
		parts = append(parts, strings.TrimSpace("Forwarded: "+s.Message.Content))
		parts = append(parts, s.Message.Parts()...)
	}
	for _, e := range m.Embeds {
		var fields []string
		for _, field := range []string{e.Title, e.Description, e.URL} {
			if field != "" {
				fields = append(fields, field)
			}
		}
		parts = append(parts, strings.TrimSpace("Embed: "+strings.Join(fields, " | ")))
	}
	for _, s := range m.Stickers {
		parts = append(parts, "Sticker: "+s.Name)
	}
	if m.Poll != nil {
		parts = append(parts, "Poll: "+m.Poll.Question.Text)
	}
	return parts
}
