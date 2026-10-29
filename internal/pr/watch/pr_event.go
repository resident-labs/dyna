package watch

import (
	"dyna-agent/internal/pr/state"
	"strings"

	"dyna-agent/internal/room"
)

// prEventOf builds the typed PR event that a delivered notice posts. The
// notice carries the facts it learned (its type, summary, commenter and
// verdict); the record carries the change facts. The body and the CI log tail
// come from the text the watcher composed for the agent, so the card and the
// agent text say the same thing.
func prEventOf(r Record, n Notice) room.PREvent {
	typ := n.Type
	if typ == "" {
		typ = noticeTypeOf(r, n)
	}
	body, tail := noticeParts(r, n.Text)
	ev := room.PREvent{
		Type:      typ,
		Label:     changeLabel(r),
		Location:  changeLocation(r),
		Number:    r.Number,
		ChangeID:  changeID(r),
		Key:       n.ID,
		Repo:      r.Repo,
		Title:     r.Title,
		Head:      r.Head,
		URL:       r.URL,
		Branch:    r.Branch,
		Summary:   n.Summary,
		Commenter: n.Commenter,
		CommentID: n.CommentID,
		Verdict:   n.Verdict,
		Detail:    noticeDetail(typ, body),
		LogTail:   tail,
	}
	if typ == room.PRCIFailed || typ == room.PRCIPaused {
		ev.CIAttempt, ev.CIMax = r.Failures, cimax
	}
	return ev
}

// cimax is the number of CI failures that pause the author's wakes.
const cimax = 3

// noticeDetail keeps the part of the watcher's body that the spec's fact line
// does not state, so the agent text says each fact once. A type whose agent
// text is the body itself keeps the whole body.
func noticeDetail(typ room.PRType, body string) string {
	switch typ {
	case room.PRApproved, room.PRCIFailed, room.PRCIPaused, room.PRCIPassed, room.PRReady, room.PROpened:
		return afterFirstParagraph(body)
	case room.PRChanges:
		return afterFirstLine(body)
	}
	return body
}

// afterFirstParagraph drops the fact paragraph of a notice body.
func afterFirstParagraph(body string) string {
	_, rest, found := strings.Cut(strings.TrimSpace(body), "\n\n")
	if !found {
		return ""
	}
	return strings.TrimSpace(rest)
}

// afterFirstLine drops the fact line of a notice body.
func afterFirstLine(body string) string {
	_, rest, found := strings.Cut(strings.TrimSpace(body), "\n")
	if !found {
		return ""
	}
	return strings.TrimSpace(rest)
}

// noticeTypeOf reads the event type of a notice that was stored before the
// typed events existed. A new notice always carries its type.
//
// TODO: legacy/shim remove later — a notice pending in a store written before
// this change has no type; remove when those stores age out.
func noticeTypeOf(r Record, n Notice) room.PRType {
	switch {
	case n.Kind == "held-comment":
		return room.PRHeldComment
	case n.Kind == "informational" || n.Kind == "radar":
		return room.PRUpdate
	case strings.Contains(n.Text, "Review PR "):
		return room.PRReviewAssigned
	case strings.Contains(n.Text, "GitHub ") && strings.Contains(n.Text, " comment "):
		return room.PRComment
	case n.Kind == "ci":
		return ciType(r)
	case strings.Contains(n.Text, " approved by its Dyna reviewer"):
		return room.PRApproved
	case strings.Contains(n.Text, "Reviewer requests changes"), strings.Contains(n.Text, "Reviewer has a question"):
		return room.PRChanges
	case strings.Contains(n.Text, " is ready to land at "):
		return room.PRReady
	case r.State == state.PRMerged:
		return room.PRMerged
	case r.State == state.PRClosed:
		return room.PRClosed
	}
	return room.PRUpdate
}

// ciType names the CI fact of a record: a pause after three failures, a
// failure, or a pass.
func ciType(r Record) room.PRType {
	if r.CIPaused {
		return room.PRCIPaused
	}
	if r.CI == state.CIFailed {
		return room.PRCIFailed
	}
	if r.CI == state.CIPassed {
		return room.PRCIPassed
	}
	return room.PRUpdate
}

// noticeParts splits a notice text into its body and its CI log tail. The
// watcher builds every notice as "<location>\nHead: <sha>\n\n<body>", so the
// header is the watcher's own structure and is not agent text.
func noticeParts(r Record, text string) (body, tail string) {
	if _, rest, found := strings.Cut(text, "\nHead: "+r.Head+"\n\n"); found {
		text = rest
	}
	if cut := strings.Index(text, "\n\nLog tail:\n"); cut >= 0 {
		return text[:cut], "Log tail:\n" + text[cut+len("\n\nLog tail:\n"):]
	}
	return text, ""
}

// reviewSummary is the short facts line of a reviewer verdict: its first
// sentence, without the markdown body.
func reviewSummary(summary string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(keepLines(summary)), "\n")
	return clip(line, 300)
}
