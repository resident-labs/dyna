package room

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/transcript"
)

// Search bounds keep one query's work small: an old session's transcript is
// outside the window, and a long run of raw matches stops before it renders
// every turn. The turn budget is a variable so a test can force the stop.
const (
	searchSessionWindow = 14 * 24 * time.Hour
	searchLineChars     = 200
	// toolOutputPrefix starts every tool-output line that WriteTurn prints.
	toolOutputPrefix = "     │ "
)

var searchTurnBudget = 200

// turnCandidate is one stored turn whose raw events hold every query word, so
// rendering it may show a matching line.
type turnCandidate struct {
	session string
	address string
	profile string
	turn    int
	started time.Time
	// structured marks a turn whose match is only the stored JSON's own keys
	// or values, never text the reader sees.
	structured bool
}

// turnHit is one rendered line of a turn that holds every query word.
type turnHit struct {
	address string
	turn    int
	at      time.Time
	line    string
}

// turnSearch is the result of scanning the room's recent transcripts.
type turnSearch struct {
	hits     []turnHit
	sessions int
	stopped  bool
}

func searchCmd() *cobra.Command {
	var limit int
	var withSessions bool
	var roomRef string
	cmd := &cobra.Command{
		Use:   "search <words> [--limit N] [--sessions]",
		Short: "Find room posts that match words, and with --sessions old turn lines",
		Long: "Find stored room posts that hold every word, newest first, ignoring case. " +
			"Each row prints the post ID, its time, its sender and the matching text, so you can " +
			"open only that post with `dyna room read`.\n\n" +
			"--sessions also searches turn transcripts: the room's open sessions and the sessions " +
			"closed in the last 14 days. Each row names the session, the turn and the matching line. " +
			"It reads the same stored turns `dyna session history` reads and never starts a turn. " +
			"Search wakes nobody and posts nothing: the rows are citations.\n\n" +
			"--limit bounds each kind: at most N posts and, with --sessions, at most N turn lines.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 || limit > 100 {
				return fmt.Errorf("--limit must be between 1 and 100")
			}
			query := strings.Join(args, " ")
			rec, err := store().ResolveContext(roomRef, false)
			if err != nil {
				return err
			}
			posts, more, err := store().SearchMessages(rec.ID, query, limit, nil)
			if err != nil {
				return err
			}
			found := turnSearch{}
			if withSessions {
				if found, err = searchTurns(rec, query, limit); err != nil {
					return err
				}
			}
			return printSearch(cmd.OutOrStdout(), rec, query, posts, more, found, withSessions)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "show at most N posts, and N turn lines with --sessions (max 100)")
	cmd.Flags().BoolVar(&withSessions, "sessions", false, "also search turn transcripts of the room's recent sessions")
	cmd.Flags().StringVar(&roomRef, "room", "", "room name or ID (default: the current room)")
	return cmd
}

func printSearch(w io.Writer, rec room.Record, query string, posts []room.Message, more bool, found turnSearch, withSessions bool) error {
	head := fmt.Sprintf("%s (%s): %s", rec.Name, rec.ID, count(len(posts), "post")+plus(more))
	if withSessions {
		head += fmt.Sprintf(", %s, %d sessions searched from the last %d days",
			count(len(found.hits), "turn line"), found.sessions, int(searchSessionWindow.Hours()/24))
	}
	if _, err := fmt.Fprintln(w, head); err != nil {
		return err
	}
	if len(posts) == 0 && len(found.hits) == 0 {
		if !found.stopped {
			if _, err := fmt.Fprintf(w, "no matches for %q\n", query); err != nil {
				return err
			}
		}
	}
	for _, msg := range posts {
		fmt.Fprintf(w, "post  %s  %s  %s -> %s [%s] %s\n", msg.ID,
			time.UnixMilli(msg.TS).Format("2006-01-02 15:04:05"), msg.From,
			strings.Join(msg.To, ", "), msg.Kind, clip(oneline(msg.Text)))
	}
	for _, hit := range found.hits {
		fmt.Fprintf(w, "turn  %s %d  %s  %s\n", hit.address, hit.turn,
			hit.at.Local().Format("2006-01-02 15:04:05"), hit.line)
	}
	if found.stopped {
		fmt.Fprintf(w, "stopped after %d turns with a raw match; add words to narrow the search\n", searchTurnBudget)
	}
	return nil
}

// searchTurns reads the bounded transcript set and returns the newest matching
// lines. It counts the sessions it read and reports when the render budget ran
// out before the limit did.
func searchTurns(rec room.Record, query string, limit int) (turnSearch, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return turnSearch{}, fmt.Errorf("search text is required")
	}
	sessions := notify.Default().Sessions.Store
	all, err := sessions.ListSnapshot(true)
	if err != nil {
		return turnSearch{}, err
	}
	cutoff := time.Now().Add(-searchSessionWindow)
	var found turnSearch
	var candidates []turnCandidate
	for _, info := range all {
		if info.RoomID != rec.ID || info.Turns == 0 {
			continue
		}
		if info.Status == subagent.StatusClosed && info.Closed.Before(cutoff) {
			continue
		}
		found.sessions++
		candidates = append(candidates, sessionCandidates(sessions, info, words)...)
	}
	// Newest first, so a tight limit reports the freshest work.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].started.After(candidates[j].started) })
	loader := transcript.NewLoader(sessions)
	rendered := 0
	// A word can live only in the stored JSON's own keys and values, such as
	// the kind of an event. Those turns render slow and rarely read well, so
	// the turns whose match is real text render first; a search that still
	// needs more hits then reads the rest, newest first, in the same order.
search:
	for pass := 0; pass < 2; pass++ {
		for _, c := range candidates {
			if len(found.hits) >= limit {
				break search
			}
			if c.structured != (pass == 1) {
				continue
			}
			if rendered == searchTurnBudget {
				found.stopped = true
				break search
			}
			rendered++
			if hit, ok := turnLine(loader, c, words); ok {
				found.hits = append(found.hits, hit)
			}
		}
	}
	return found, nil
}

// turnLine renders one candidate turn and returns its first matching line.
func turnLine(loader *transcript.Loader, c turnCandidate, words []string) (turnHit, bool) {
	loaded, err := loader.LoadTurn(c.session, c.turn)
	if err != nil {
		return turnHit{}, false
	}
	hit := turnHit{address: c.address, turn: c.turn, at: c.started}
	for _, turn := range loaded.Turns {
		var text strings.Builder
		transcript.WriteTurn(&text, turn, transcript.PlainOptions{Agent: c.profile, Expand: true})
		var prose, detail string
		for _, line := range strings.Split(strings.TrimSuffix(text.String(), "\n"), "\n") {
			if !holdsWordsInLine(line, words) {
				continue
			}
			// A conversation line reads better than a tool-output line, so the
			// turn reports its output only when nothing else matched.
			if strings.HasPrefix(line, toolOutputPrefix) {
				if detail == "" {
					detail = line
				}
				continue
			}
			prose = line
			break
		}
		if prose == "" {
			prose = detail
		}
		if prose != "" {
			hit.line = clip(oneline(prose))
			return hit, true
		}
	}
	return turnHit{}, false
}

// sessionCandidates returns the stored turns of one session that may show a
// matching line. A turn is a candidate when its raw events, or the turn
// record's own text, holds every word. Both are a cheap filter over the same
// stored turn `dyna session history` reads, without building it.
func sessionCandidates(sessions *subagent.Store, info subagent.Info, words []string) []turnCandidate {
	// A turn that never wrote output still has a prompt, and a stopped harness
	// may leave no output file at all, so a missing file is not an error.
	var raw []byte
	if path, err := sessions.NormalizedPath(info.ID); err == nil {
		if b, err := os.ReadFile(path); err == nil {
			raw = b
		}
	}
	turns, err := sessions.Turns(info.ID)
	if err != nil {
		return nil
	}
	var out []turnCandidate
	for i, turn := range turns {
		end := turn.NormEnd
		if end == 0 && i+1 < len(turns) {
			end = turns[i+1].NormStart
		}
		if end == 0 {
			end = int64(len(raw))
		}
		held, content := false, false
		if turn.NormStart >= 0 && end <= int64(len(raw)) && end >= turn.NormStart {
			held, content = holdsWordsInBytes(raw[turn.NormStart:end], words)
		}
		if !held {
			// The record's own fields render without any stored output.
			held, content = holdsWordsInBytes(turnText(turn), words)
		}
		if !held {
			continue
		}
		at := turn.Started
		if at.IsZero() {
			at = info.LastActivity
		}
		out = append(out, turnCandidate{session: info.ID, address: info.Address(), profile: info.Profile,
			turn: turn.N, started: at, structured: !content})
	}
	return out
}

// turnText is the readable text a turn record holds by itself: its prompt, its
// steering messages, its result and its error.
func turnText(turn subagent.Turn) []byte {
	var b strings.Builder
	b.WriteString(turn.Prompt)
	for _, steer := range turn.Steering {
		b.WriteString("\n")
		b.WriteString(steer.Message)
	}
	if turn.Result != nil {
		b.WriteString("\n")
		fmt.Fprint(&b, turn.Result)
	}
	b.WriteString("\n")
	b.WriteString(turn.Error)
	return []byte(b.String())
}

// holdsWordsInLine reports whether one line holds every word, ignoring case.
func holdsWordsInLine(line string, words []string) bool {
	text := strings.ToLower(line)
	for _, word := range words {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

// holdsWordsInBytes reports whether b holds every word, ignoring ASCII case.
// content reports that at least one word match is text rather than a bare JSON
// key or value.
func holdsWordsInBytes(b []byte, words []string) (held, content bool) {
	held, content = true, false
	for _, word := range words {
		found, text := containsFold(b, word)
		if !found {
			return false, false
		}
		content = content || text
	}
	return held, content
}

// containsFold finds word in b, ignoring ASCII case, and reports whether the
// match sits outside a JSON string. It jumps with IndexByte, which is
// vectorized, so a large transcript stays cheap to scan.
func containsFold(b []byte, word string) (found, content bool) {
	if word == "" {
		return true, true
	}
	first := word[0]
	for i := 0; i+len(word) <= len(b); {
		next := bytes.IndexByte(b[i:], lowerASCII(first))
		if upper := bytes.IndexByte(b[i:], upperASCII(first)); upper >= 0 && (next < 0 || upper < next) {
			next = upper
		}
		if next < 0 {
			return found, false
		}
		i += next
		if i+len(word) > len(b) {
			return found, false
		}
		if equalFoldASCII(b[i:i+len(word)], word) {
			if !quotedAt(b, i, i+len(word)) {
				return true, true
			}
			found = true
		}
		i++
	}
	return found, false
}

// quotedAt reports whether b[start:end] is a whole JSON string, as a key or a
// value is. A nested encoded string closes with a backslash before its quote.
func quotedAt(b []byte, start, end int) bool {
	if start == 0 || b[start-1] != '"' {
		return false
	}
	return end < len(b) && (b[end] == '"' || (b[end] == '\\' && end+1 < len(b) && b[end+1] == '"'))
}

func equalFoldASCII(b []byte, word string) bool {
	for i := 0; i < len(word); i++ {
		if lowerASCII(b[i]) != lowerASCII(word[i]) {
			return false
		}
	}
	return true
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

func upperASCII(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

// clip keeps a search row one line and bounded in length.
func clip(line string) string {
	runes := []rune(line)
	if len(runes) <= searchLineChars {
		return line
	}
	return string(runes[:searchLineChars]) + "…"
}

// count writes a number with its noun, singular when the number is one.
func count(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// plus marks a bounded count with older results behind it.
func plus(more bool) string {
	if more {
		return "+"
	}
	return ""
}
