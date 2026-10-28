package roomsvc

import (
	"dyna-agent/internal/terminalsvc"
	"encoding/json"
	"fmt"
	"strings"

	"dyna-agent/internal/room"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/turntext"

	lru "github.com/hashicorp/golang-lru/v2"
)

// MaxFeedResultKB caps the full result one feed row carries, so one giant
// result cannot stall the page.
const MaxFeedResultKB = turntext.MaxFeedResultKB

// resultCacheEntries bounds retained full results to 256, or 16 MiB when
// every result reaches MaxFeedResultKB.
const resultCacheEntries = 256

type resultCache = lru.Cache[string, fullResult]

func newResultCache() *resultCache {
	cache, err := lru.New[string, fullResult](resultCacheEntries)
	if err != nil {
		panic(err) // the entry count is a positive constant
	}
	return cache
}

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
	p = s.expandResult(p, book)
	// A post the owner wrote in another room names that room, so the feed marks
	// it as a message from there. Agents' cross-room posts stay as they are.
	if p.Human && p.SenderRoomID != "" && p.SenderRoomID != p.RoomID {
		if from, err := s.rooms().Resolve(p.SenderRoomID); err == nil {
			p.FromRoom = from.Name
		}
	}
	return p
}

func (s *Service) expandResult(p Post, book avatarBook) Post {
	if ev, ok := p.Shell(); ok {
		return s.expandShellEvent(p, ev)
	}
	if ev, ok := p.RunResult(); ok {
		return s.expandRunEvent(p, ev)
	}
	// TODO: legacy/shim remove later — a completion stored before the typed
	// shell, run and turn events names its job in the sender. Remove when
	// retained room history ages out.
	if p.Completion && strings.HasPrefix(p.From, "shell:") {
		return s.expandShell(p)
	}
	if p.Completion && strings.HasPrefix(p.From, "run:") {
		return s.expandRun(p)
	}
	if !p.Completion || p.Turn <= 0 {
		return p
	}
	// A typed turn event names its session in the payload; an older completion
	// names it in the sender.
	author := p.From
	if ev, ok := p.TurnResult(); ok {
		author = ev.Session
	}
	if r, ok := s.results.Get(p.ID); ok {
		return withFull(p, r)
	}
	if ev, ok := p.TurnResult(); ok && ev.SessionID != "" {
		if text, cut, ok := turntext.Render(s.sessions(), ev.SessionID, ev.Session, ev.Turn, string(ev.State)); ok {
			r := fullResult{text: text, cut: cut, detail: &PostDetail{Kind: "session", ID: ev.SessionID, Turn: ev.Turn}}
			s.results.Add(p.ID, r)
			return withFull(p, r)
		}
		return p
	}
	// The writer is the session at this address whose turn p.Turn ended
	// last before the post: a completion is posted right after its turn
	// ends. Creation times are too coarse to tell a reused name apart.
	var turn subagent.Turn
	var sessionID string
	found := false
	for _, id := range book.sessionsFor(author) {
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
	r.text, r.cut = FullCompletionText(author, p.Turn, completionState(p.Message), result, turn.Error)
	s.results.Add(p.ID, r)
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
	return turntext.FullCompletionText(address, turn, kind, result, errText)
}

// expandRun names a workflow run's completion by the run's name and shows
// its whole result or error, as the run view does.
func (s *Service) expandRun(p Post) Post {
	id := strings.TrimPrefix(p.From, "run:")
	if r, ok := s.results.Get(p.ID); ok {
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
	s.results.Add(p.ID, r)
	p.Speaker = r.speaker
	return withFull(p, r)
}

// completionState names the state a completion post reports: the typed turn
// event's own state, or the kind of a completion stored before it.
func completionState(m room.Message) string {
	if ev, ok := m.TurnResult(); ok {
		return string(ev.State)
	}
	return m.Kind
}

// expandShellEvent links a typed shell result to its stored job and writes the
// row's own sentence over the agent text: the command's label, how it ended,
// and its output. The shell ID stays out of what the owner reads.
func (s *Service) expandShellEvent(p Post, ev room.ShellEvent) Post {
	p.Detail = &PostDetail{Kind: "shell", ID: ev.ShellID}
	if ev.Label != "" {
		p.Speaker = ev.Label
	}
	text := fmt.Sprintf("%s with exit code %d", ev.State, ev.ExitCode)
	if len(ev.Output) > 0 {
		text += "\nLast output:\n```\n" + strings.Join(ev.Output, "\n") + "\n```"
	}
	p.Text, p.Preview, p.Hidden = text, "", 0
	return p
}

// expandRunEvent links a typed run result to its run view and writes the run's
// own result over the agent text.
func (s *Service) expandRunEvent(p Post, ev room.RunEvent) Post {
	p.Detail = &PostDetail{Kind: "run", ID: ev.RunID}
	if ev.Name != "" {
		p.Speaker = "workflow " + ev.Name
	}
	p.Text = runResultText(ev)
	p.FullResult, p.Preview, p.Hidden = true, "", 0
	return p
}

// runResultText is what a feed shows for a run: how it ended and its result.
func runResultText(ev room.RunEvent) string {
	switch ev.State {
	case room.RunStopped:
		return "Canceled"
	case room.RunFailed:
		if ev.Summary != "" {
			return "Error: " + ev.Summary
		}
		return "Error"
	}
	if ev.Summary == "" {
		return "No result."
	}
	return ev.Summary
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
