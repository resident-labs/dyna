package room

import "fmt"

// A visualization is a chain of card posts. The post that created it names
// it, and each update is a later post whose card carries that ID in Visual.
// Clients show the newest version wherever the visualization appears.

// VisualID names the visualization a card in post postID belongs to.
func (a Attachment) VisualID(postID string) string {
	if a.Visual != "" {
		return a.Visual
	}
	return postID
}

// updatesVisual reports a card that names an earlier version.
func updatesVisual(atts []Attachment) bool {
	for _, a := range atts {
		if a.Rich() && a.Visual != "" {
			return true
		}
	}
	return false
}

// stampVersions sets Version on each card in atts and resolves Visual to
// the first post. msgs is the room log; the caller holds the room lock, so
// two updates cannot take the same number.
func stampVersions(msgs []Message, atts []Attachment) error {
	for i := range atts {
		a := &atts[i]
		if !a.Rich() {
			continue
		}
		if a.Visual == "" {
			a.Version = 1
			continue
		}
		root := ""
		for _, m := range msgs {
			if m.ID != a.Visual {
				continue
			}
			for _, c := range m.Attachments {
				if c.Rich() {
					root = m.ID
					if c.Visual != "" {
						root = c.Visual
					}
				}
			}
		}
		if root == "" {
			return fmt.Errorf("post %s is not a visual in this room", a.Visual)
		}
		a.Visual, a.Version = root, 1
		for _, m := range msgs {
			for _, c := range m.Attachments {
				if c.Rich() && (m.ID == root || c.Visual == root) {
					a.Version++
				}
			}
		}
	}
	return nil
}

// unversioned drops the version number, which a retry counts again, so a
// retried post compares equal to the one it stored.
func unversioned(atts []Attachment) []Attachment {
	out := append([]Attachment(nil), atts...)
	for i := range out {
		out[i].Version = 0
	}
	return out
}
