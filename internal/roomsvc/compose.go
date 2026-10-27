package roomsvc

import (
	"context"
	"errors"
	"sort"
	"strings"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// Mentions the composer offers besides members. TeamMention reaches the
// owner's open top-level agents; a draft with no mention goes to RootMention.
const (
	TeamMention = "@team"
	RootMention = "@root"
)

// Draft is a parsed composer draft.
type Draft struct {
	// Mentions are the tokens the post goes to, in order.
	Mentions []string `json:"mentions"`
	// Text is the post body: the draft without its leading mentions.
	Text string `json:"text"`
	// Default is set when the draft named nobody and goes to @root.
	Default bool `json:"default"`
	// Wakes names who the post should wake, for the line above the
	// composer. Preview and Post use the same routing rules.
	Wakes   []string      `json:"wakes"`
	Skipped []notify.Skip `json:"skipped,omitempty"`
	// Self is set when the post goes to the owner alone. No session wakes,
	// but an external orchestrator that watches the room receives it;
	// Wakes then names "root".
	Self bool `json:"self"`
	// Spawn is set when the draft is "/<profile> <task>": Enter starts a
	// session instead of posting. Mentions, Wakes and Self are then unused.
	Spawn *SpawnPreview `json:"spawn,omitempty"`
}

// Sticky is what a composer keeps after this draft is sent: its explicit
// recipients, so the next post goes to the same members. A draft that named
// nobody (the @root default) keeps nothing.
func (d Draft) Sticky() string {
	if d.Default || len(d.Mentions) == 0 {
		return ""
	}
	return strings.Join(d.Mentions, " ") + " "
}

// Choices lists the mentions the composer completes in a room: @team, @root,
// and each open member by name.
func Choices(members []string) []string {
	out := []string{TeamMention, RootMention}
	for _, n := range members {
		if "@"+n == RootMention || "@"+n == TeamMention {
			continue
		}
		out = append(out, "@"+n)
	}
	sort.Strings(out[2:])
	return out
}

// ParseDraft reads a composer draft for the owner of roomName. members are
// the names of the room's open sessions.
//
// Leading @mentions come from room.SplitLeadingMentionText, the same rule
// as dyna post. A mention may end in punctuation: "@lead," is @lead. Later in the text, an @token is a mention too when it
// names @team or an open member; it stays in the text. Such a token starts
// the text or follows a space or "(", and may end in punctuation. Tokens
// inside `code` and tokens written \@name are text. A draft with no mention
// goes to @root, so the owner never wakes every top-level agent by accident.
func ParseDraft(draft, roomName string, members []string) Draft {
	var d Draft
	mentions, rest := room.SplitLeadingMentionText(strings.TrimSpace(draft))
	for _, word := range mentions {
		d.Mentions = append(d.Mentions, trimToken(word))
	}
	known := map[string]bool{TeamMention: true}
	for _, n := range members {
		known["@"+n], known["@"+roomName+":"+n] = true, true
	}
	d.Text = inlineMentions(rest, known, &d.Mentions)
	if len(d.Mentions) == 0 {
		d.Mentions, d.Default = []string{RootMention}, true
	}
	return d
}

// inlineMentions appends each known @token in text to mentions and returns
// the text with \@ escapes removed.
func inlineMentions(text string, known map[string]bool, mentions *[]string) string {
	seen := map[string]bool{}
	for _, m := range *mentions {
		seen[m] = true
	}
	var b strings.Builder
	codeRun := 0
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '`':
			// A code span closes only with the same backtick run that
			// opened it. Filenames can contain shorter backtick runs.
			j := i + 1
			for j < len(text) && text[j] == '`' {
				j++
			}
			if codeRun == 0 {
				codeRun = j - i
			} else if codeRun == j-i {
				codeRun = 0
			}
			b.WriteString(text[i:j])
			i = j - 1
			continue
		case codeRun > 0:
		case c == '\\' && i+1 < len(text) && text[i+1] == '@':
			continue
		case c == '@' && (i == 0 || strings.IndexByte(" \t\n(", text[i-1]) >= 0):
			j := i + 1
			for j < len(text) && tokenByte(text[j]) {
				j++
			}
			tok := trimToken(text[i:j])
			if known[tok] && !seen[tok] {
				seen[tok] = true
				*mentions = append(*mentions, tok)
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// trimToken drops punctuation that ends a sentence or a clause from a
// mention, as in "@lead," or "@lead:".
func trimToken(tok string) string {
	if t := strings.TrimRight(tok, ".,;:!?"); len(t) > 1 {
		return t
	}
	return tok
}

func tokenByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("-_.:", c) >= 0
}

// Preview parses a draft against the room's open members, for the line
// above a composer. It uses the same spawn parse as Send, so the line never
// disagrees with what Enter does.
func (s *Service) Preview(roomRef, draft string) (Draft, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return Draft{}, err
	}
	if sp := s.previewSpawn(rec, draft); sp != nil {
		return Draft{Mentions: []string{}, Wakes: []string{}, Text: sp.Task, Spawn: sp}, nil
	}
	return s.parse(rec, UnescapeSlash(draft))
}

// parse reads a post draft (never a spawn) against the room's open members.
func (s *Service) parse(rec room.Record, draft string) (Draft, error) {
	members, err := s.sessions().ListSnapshot(false)
	if err != nil {
		return Draft{}, err
	}
	var names []string
	for _, m := range members {
		if m.RoomID == rec.ID && !m.RunOwned() {
			names = append(names, m.EffectiveName())
		}
	}
	d := ParseDraft(draft, rec.Name, names)
	preview, err := s.Notify.Preview(notify.Sender{Kind: notify.SenderOwner, Room: rec.Name, Human: true}, d.Mentions)
	if err != nil {
		return d, err
	}
	d.Skipped = preview.Skipped
	d.Wakes = []string{}
	for _, addr := range preview.Recipients {
		if addr == rec.Name {
			d.Wakes = append(d.Wakes, "root")
		} else {
			d.Wakes = append(d.Wakes, strings.TrimPrefix(addr, rec.Name+":"))
		}
	}
	d.Self = len(preview.Recipients) == 1 && preview.Recipients[0] == rec.Name
	return d, nil
}

// Post sends a draft as the human room owner, through the same delivery
// service as dyna post. A draft with no mention goes to @root, and a draft
// to @root alone wakes no session; it reaches external orchestrators that
// watch the room, because they share the owner address with the human.
// Attachments must be saved in the room first (Attach); mentions come from
// the draft text only, never from the files.
func (s *Service) Post(ctx context.Context, roomRef, draft, replyTo string, atts ...room.Attachment) (PostResult, error) {
	return s.postIn(ctx, roomRef, draft, replyTo, "", "", false, nil, atts...)
}

// PostFromCard is Post for text that an HTML card wrote and the owner
// confirmed. The stored post carries room.SourceCard.
func (s *Service) PostFromCard(ctx context.Context, roomRef, draft, replyTo string, conversation room.Conversation) (PostResult, error) {
	return s.postIn(ctx, roomRef, draft, replyTo, "", conversation, true, nil)
}

// postIn posts one composer draft in the conversation its writer named: the
// room feed when conversation is empty.
func (s *Service) postIn(ctx context.Context, roomRef, draft, replyTo, forwardOf string, conversation room.Conversation, card bool, refs []room.Reference, atts ...room.Attachment) (PostResult, error) {
	// A spawn-shaped draft is a command, never text: posting "/opus fix it"
	// to @root would be a silent misfire. Send routes it; "//" escapes it.
	if _, _, ok := splitSpawn(draft); ok {
		return PostResult{}, errors.New("this draft starts a session; send it with Send, or start it with // to post a slash")
	}
	draft = UnescapeSlash(draft)
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return PostResult{}, err
	}
	d, err := s.parse(rec, draft)
	if err != nil {
		return PostResult{}, err
	}
	if strings.TrimSpace(d.Text) == "" && len(atts) == 0 && len(refs) == 0 && forwardOf == "" {
		return PostResult{}, errors.New("type a post after the mentions")
	}
	res, err := s.Notify.Post(ctx, notify.PostInput{
		Sender:   notify.Sender{Kind: notify.SenderOwner, Room: rec.Name, Human: true, Card: card},
		Mentions: d.Mentions, Text: strings.TrimSpace(d.Text), ReplyTo: replyTo, ForwardOf: forwardOf, Attachments: atts, References: refs, Conversation: conversation,
	})
	out := PostResult{Message: res.Message, Deliveries: res.Deliveries, Failed: res.Failed(), Sticky: d.Sticky()}
	if res.Message.ID != "" {
		_ = s.rooms().MarkOwnerRead(res.Message.RoomID, res.Message.ID)
	}
	// A stored post is sent, so the composer keeps it and shows the failed
	// deliveries in Failed instead of offering the same text again.
	if errors.Is(err, notify.ErrDelivery) && res.Message.ID != "" {
		err = nil
	}
	return out, err
}

// MentionChoices lists the mentions the composer can complete in a room. A
// service session is no member, but a post may still address it by name, so
// its open top-level sessions are added here.
func (s *Service) MentionChoices(ref string) ([]string, error) {
	rec, err := s.rooms().Resolve(ref)
	if err != nil {
		return nil, err
	}
	members, err := s.Members(ref, false)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range members[1:] {
		if m.Closed {
			continue
		}
		names = append(names, m.Name)
	}
	live, err := s.sessions().ListSnapshot(false)
	if err != nil {
		return nil, err
	}
	for _, info := range live {
		if info.ManagedService() && info.ParentSession == "" && info.RoomID == rec.ID && info.Status != subagent.StatusClosed {
			names = append(names, info.EffectiveName())
		}
	}
	return Choices(names), nil
}
