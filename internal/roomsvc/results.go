package roomsvc

import (
	"dyna-agent/internal/terminalsvc"
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
)

// MaxFeedResultKB caps the full result one feed row carries, so one giant
// result cannot stall the page.
const MaxFeedResultKB = 64

type fullResult struct {
	text    string
	cut     bool
	speaker string // set for workflow runs: "workflow <name>"
	detail  *PostDetail
}

// expand replaces a session completion's stored preview with the turn's full
// result, read from the session store like `dyna subagent output`. Only the
// UI calls it; the stored post, and what agents read, stay short.
func (s *Service) expand(p Post, book avatarBook) Post {
	if p.Completion && strings.HasPrefix(p.From, "shell:") {
		return s.expandShell(p)
	}
	if p.Completion && strings.HasPrefix(p.From, "run:") {
		return s.expandRun(p)
	}
	if !p.Completion || p.Turn <= 0 {
		return p
	}
	if v, ok := s.results.Load(p.ID); ok {
		r := v.(fullResult)
		return withFull(p, r)
	}
	// The writer is the session at this address whose turn p.Turn ended
	// last before the post: a completion is posted right after its turn
	// ends. Creation times are too coarse to tell a reused name apart.
	var turn subagent.Turn
	var sessionID string
	found := false
	for _, id := range book.sessionsFor(p.From) {
		t, err := s.sessions().ReadTurn(id, p.Turn)
		if err != nil || !t.Done() || t.Ended.UnixMilli() > p.TS {
			continue
		}
		if !found || t.Ended.After(turn.Ended) {
			turn, found = t, true
			sessionID = id
		}
	}
	if !found {
		return p
	}
	r := fullResult{detail: &PostDetail{Kind: "session", ID: sessionID, Turn: p.Turn}}
	result := turn.Result
	if meta, err := s.sessions().ReadMeta(sessionID); err == nil {
		if text, ok := serviceArtifactText(meta, result); ok {
			result = text
		}
	}
	r.text, r.cut = FullCompletionText(p.From, p.Turn, p.Kind, result, turn.Error)
	s.results.Store(p.ID, r)
	return withFull(p, r)
}

// withFull puts the full result in a post. The fold fields described the
// stored preview, so they are cleared: a full result is shown whole.
func withFull(p Post, r fullResult) Post {
	p.Text, p.FullResult, p.ResultCut = r.text, true, r.cut
	p.Detail = r.detail
	p.Preview, p.Hidden = "", 0
	return p
}

// FullCompletionText is a completion body with the whole result: the same
// header as the stored post, then the result as text, or JSON in a fence,
// or the error. It is clipped at MaxFeedResultKB with a note.
func FullCompletionText(address string, turn int, kind string, result any, errText string) (string, bool) {
	head := fmt.Sprintf("%s turn %d %s.", address, turn, kind)
	var body string
	switch v := result.(type) {
	case nil:
	case string:
		body = strings.TrimSpace(v)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err == nil {
			body = "```json\n" + string(b) + "\n```"
		}
	}
	text := head
	if errText != "" {
		text += " Error: " + strings.TrimSpace(errText)
	}
	if body != "" {
		text += " Result:\n\n" + body
	}
	max := MaxFeedResultKB * 1024
	if len(text) <= max {
		return text, false
	}
	cut := strings.ToValidUTF8(text[:max], "")
	if strings.Count(cut, "```")%2 == 1 {
		cut += "\n```"
	}
	return cut + fmt.Sprintf("\n\n*Cut at %d KB. Open the transcript for more.*", MaxFeedResultKB), true
}

// expandRun names a workflow run's completion by the run's name and shows
// its whole result or error, as the run view does.
func (s *Service) expandRun(p Post) Post {
	id := strings.TrimPrefix(p.From, "run:")
	if v, ok := s.results.Load(p.ID); ok {
		r := v.(fullResult)
		p.Speaker = r.speaker
		return withFull(p, r)
	}
	m, err := runstore.ReadMeta(id)
	if err != nil {
		return p
	}
	r := fullResult{speaker: "workflow " + m.Name, detail: &PostDetail{Kind: "run", ID: id}}
	switch {
	case m.Status == "canceled":
		r.text = "Canceled"
	case m.Error != "":
		r.text = "Error: " + strings.TrimSpace(m.Error)
	default:
		res, _ := runstore.ReadResult(id)
		var v any
		if json.Unmarshal([]byte(res), &v) == nil && v != nil {
			if str, ok := v.(string); ok {
				r.text = strings.TrimSpace(str)
			} else if b, err := json.MarshalIndent(v, "", "  "); err == nil {
				r.text = "```json\n" + string(b) + "\n```"
			}
		}
	}
	if r.text == "" {
		r.text = "No result."
	}
	if max := MaxFeedResultKB * 1024; len(r.text) > max {
		r.text, r.cut = strings.ToValidUTF8(r.text[:max], "")+fmt.Sprintf("\n\n*Cut at %d KB. Open the workflow for more.*", MaxFeedResultKB), true
	}
	s.results.Store(p.ID, r)
	p.Speaker = r.speaker
	return withFull(p, r)
}

// expandShell keeps the exit result and links to its stored log. Agent-facing
// notifications keep the command pointer in the room log.
func (s *Service) expandShell(p Post) Post {
	id := strings.TrimPrefix(p.From, "shell:")
	m, err := terminalsvc.ShellStore(s.sessions().Root).ReadMeta(id)
	if err != nil || m.RoomID != p.RoomID {
		return p
	}
	p.Detail = &PostDetail{Kind: "shell", ID: id}
	p.Text = strings.TrimSuffix(p.Text, "\nOutput: dyna shell tail "+id)
	p.Text = strings.TrimPrefix(p.Text, p.From+" ")
	if m.Name != "" {
		// The description is the speaker, so the text need not repeat it.
		p.Text = strings.Replace(p.Text, ": "+m.Name, "", 1)
	}
	p.Preview, p.Hidden = "", 0
	if m.Name != "" {
		p.Speaker = m.Name
	}
	return p
}
