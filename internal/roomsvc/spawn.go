package roomsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// ProfileChoice is one profile the composer offers after "/". It carries no
// command, arguments or environment: those can hold secrets, and a web
// client will read this payload too.
type ProfileChoice struct {
	Name    string   `json:"name"`
	Harness string   `json:"harness"`
	Model   string   `json:"model,omitempty"`
	Models  []string `json:"models,omitempty"`
	Default bool     `json:"default,omitempty"`
}

// Profiles lists the enabled profiles a session can start with, in registry
// order. Services-only profiles are left out.
func (s *Service) Profiles() ([]ProfileChoice, error) {
	store, err := s.Notify.Sessions.ProfileStore()
	if err != nil {
		return nil, err
	}
	out := []ProfileChoice{}
	for _, p := range store.Profiles {
		if !p.ForSubagents() {
			continue
		}
		out = append(out, ProfileChoice{Name: p.Name, Harness: p.Harness, Model: p.Model, Models: p.EffectiveModels(), Default: p.Default})
	}
	return out, nil
}

// SpawnPreview is a parsed "/<profile> <task>" draft: what Enter would start.
// Error is set when the draft is a spawn attempt that cannot start; it
// blocks the send, so a typo never falls through to a plain post.
type SpawnPreview struct {
	Profile string `json:"profile"`
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
	// Name is the predicted session name. StartSession may add a suffix if
	// another session takes the name first.
	Name  string `json:"name,omitempty"`
	Task  string `json:"task,omitempty"`
	Dir   string `json:"dir,omitempty"`
	Room  string `json:"room"`
	Error string `json:"error,omitempty"`
}

// profileShaped matches a token that looks like a profile name, as opposed
// to a path such as /home/user or a URL fragment.
var profileShaped = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// splitSpawn reads "/<profile> <task>". ok is false when the draft is not a
// spawn attempt and should post as text: it does not start with "/", starts
// with "//" (an escaped slash), or its first token is not profile-shaped.
func splitSpawn(draft string) (prof, task string, ok bool) {
	d := strings.TrimLeft(draft, " \t\n")
	if !strings.HasPrefix(d, "/") || strings.HasPrefix(d, "//") {
		return "", "", false
	}
	d = d[1:]
	prof, task = d, ""
	if i := strings.IndexAny(d, " \t\n"); i >= 0 {
		prof, task = d[:i], d[i:]
	}
	task = strings.TrimSpace(task)
	// A bare "/" is the start of a command being typed, not a post of "/".
	if prof == "" && task == "" {
		return "", "", true
	}
	if !profileShaped.MatchString(prof) {
		return "", "", false
	}
	return prof, task, true
}

// UnescapeSlash turns a draft that starts with "//" into one that starts
// with "/", so "//help" posts the text "/help".
func UnescapeSlash(draft string) string {
	t := strings.TrimLeft(draft, " \t\n")
	if strings.HasPrefix(t, "//") {
		return t[1:]
	}
	return draft
}

// previewSpawn parses a spawn draft against the room and the registry. It
// returns nil when the draft is a normal post.
func (s *Service) previewSpawn(rec room.Record, draft string) *SpawnPreview {
	prof, task, ok := splitSpawn(draft)
	if !ok {
		return nil
	}
	p := &SpawnPreview{Profile: prof, Task: task, Room: rec.Name}
	profiles, err := s.Profiles()
	if err != nil {
		p.Error = err.Error()
		return p
	}
	var found *ProfileChoice
	for i := range profiles {
		if profiles[i].Name == prof {
			found = &profiles[i]
		}
	}
	switch {
	case prof == "":
		p.Error = "type a profile name after /, or start with // to post a slash"
		return p
	case found == nil:
		p.Error = fmt.Sprintf("no enabled profile is named %q. Type / to pick one, or start with // to post a slash", prof)
		return p
	case task == "":
		p.Harness, p.Model = found.Harness, found.Model
		p.Error = "type the task after the profile"
		return p
	}
	p.Harness, p.Model = found.Harness, found.Model
	p.Dir, err = roomDir(rec)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	taken := map[string]bool{}
	if members, err := s.Members(rec.ID, false); err == nil {
		for _, m := range members[1:] {
			taken[m.Name] = true
		}
	}
	p.Name = freeName(SuggestName(task), taken)
	return p
}

// roomDir validates the working directory for a session the owner starts.
// Rooms need a repo.
func roomDir(rec room.Record) (string, error) {
	dir := rec.Repo
	if dir == "" {
		return "", fmt.Errorf("#%s has no repo, so a new session has no safe working directory. Use a room made with `dyna room create --repo <path>`", rec.Name)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("the repo of #%s, %s, is not a directory", rec.Name, dir)
	}
	return dir, nil
}

// maxSuffix bounds the "-2", "-3" retries before StartSession gives up on
// the suggested name and lets Dyna pick agent-<id>.
const maxSuffix = 9

// freeName returns base, or base-N for the first N in 2..maxSuffix that is
// not taken. An empty base, or one with no free suffix, returns "".
func freeName(base string, taken map[string]bool) string {
	if base == "" || !taken[base] {
		return base
	}
	for n := 2; n <= maxSuffix; n++ {
		if c := fmt.Sprintf("%s-%d", base, n); !taken[c] {
			return c
		}
	}
	return ""
}

// skipWords are words that say little about what a session is for:
// articles, pronouns, auxiliaries, and the filler verbs tasks start with.
var skipWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the this that these those it its i me my we our you your
		he she they them their is are was were be been being am do does did done have has had
		can could should would will shall may might must to of in on at by for from with without
		into onto about over under and or but not no so if then than as also just all any some
		why how what when where which who whom whose there here up out
		please find fix make check look investigate add write review implement create build update
		help try see get let use run read figure explain tell show give keep go start want need`) {
		skipWords[w] = true
	}
}

// SuggestName picks a session name from a task, in Dyna's idiomatic style:
// a short kebab-case role label such as "parser" or "decoder-rejects". It
// keeps the first two words that carry meaning. If none do, it returns "",
// and Dyna falls back to agent-<id>.
func SuggestName(task string) string {
	var words []string
	for _, raw := range strings.Fields(strings.ToLower(task)) {
		// Keep letters, digits and inner dashes: "crc-32," becomes "crc-32".
		w := strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
				return r
			}
			return -1
		}, raw)
		w = strings.Trim(w, "-")
		if len(w) < 2 || skipWords[w] || strings.ContainsAny(raw, "/@`") {
			continue
		}
		words = append(words, w)
		if len(words) == 2 {
			break
		}
	}
	name := strings.Join(words, "-")
	if len(name) > 32 {
		name = strings.TrimRight(name[:32], "-")
	}
	if name == "" || name[0] < 'a' || name[0] > 'z' || subagent.ValidateName(name) != nil {
		return ""
	}
	return name
}

// SpawnResult is a session the owner started from the composer.
type SpawnResult struct {
	Member Member `json:"member"`
	// Warning is set when the session started but its feed record failed.
	Warning string `json:"warning,omitempty"`
}

// StartSession starts a top-level session from a "/<profile> <task>" draft,
// as the room owner. The draft is parsed again here: the preview a client
// showed is a prediction, the result is the truth. The session works in the
// room's working directory and joins this room. With files, it first records
// the owner's task and attachments, then passes their paths in the prompt.
func (s *Service) StartSession(ctx context.Context, roomRef, draft string, atts ...room.Attachment) (SpawnResult, error) {
	rec, err := s.rooms().Resolve(roomRef)
	if err != nil {
		return SpawnResult{}, err
	}
	p := s.previewSpawn(rec, draft)
	if p == nil {
		return SpawnResult{}, errors.New("a session starts from /<profile> <task>")
	}
	if p.Error != "" {
		return SpawnResult{}, errors.New(p.Error)
	}
	start := *p
	var request room.Message
	if len(atts) > 0 {
		filesDir, err := s.rooms().FilesDir(rec.ID)
		if err != nil {
			return SpawnResult{}, err
		}
		filesDir, err = filepath.Abs(filesDir)
		if err != nil {
			return SpawnResult{}, err
		}
		// Save a normal owner post before launching the session. It targets
		// only the owner; the task reaches the agent once, in its first prompt.
		request, err = s.rooms().Post(rec.ID, room.PostInput{
			From: rec.Name, Human: true, To: []string{rec.Name}, Text: p.Task, Attachments: atts,
		})
		if err != nil {
			return SpawnResult{}, err
		}
		_ = s.rooms().MarkOwnerRead(rec.ID, request.ID)
		start.Task = request.Frame(room.FrameOptions{Room: rec.Name, FilesDir: filesDir})
	}
	info, err := s.startNamed(ctx, rec, &start, SuggestName(p.Task))
	if err != nil {
		if request.ID != "" {
			return SpawnResult{}, fmt.Errorf("the task and files were saved as post %s, but the session could not start: %w", request.ID, err)
		}
		return SpawnResult{}, err
	}
	out := SpawnResult{Member: s.member(info, 0, 0)}
	out.Member.Emoji = s.avatars(rec.ID).byID[info.ID]
	// Start puts the task only in the turn 1 prompt. This record shows the
	// owner's request in the feed. It is addressed to the owner alone, so the
	// turn gate never delivers it to the new session as steering.
	// The desktop shows posts in full, so the record carries a clipped task;
	// the profile card holds the whole one.
	text := fmt.Sprintf("Started @%s (%s) in %s:\n\n%s", info.EffectiveName(), info.Profile, info.Dir, clip(p.Task, 3, 280))
	if _, err := s.rooms().Post(rec.ID, room.PostInput{From: rec.Name, Kind: KindSpawn, Human: true, To: []string{rec.Name}, Text: text}); err != nil {
		out.Warning = "the session started, but its feed record failed: " + err.Error()
	}
	return out, nil
}

// KindSpawn marks the owner's feed record of a session started from the
// composer.
const KindSpawn = "spawn"

// startNamed starts the session as base, then base-2 .. base-9 while another
// open session holds the name, then as "" so Dyna picks agent-<id>. Start
// rechecks names under its lock, so the retry is race-safe and an automatic
// name never fails a spawn.
func (s *Service) startNamed(ctx context.Context, rec room.Record, p *SpawnPreview, base string) (subagent.Info, error) {
	// The composer takes one request, so the goal restates it.
	goal := fmt.Sprintf("Complete the owner's request: %s; done when the result is reported.", goals.Clip(p.Task, 300))
	opts := subagent.StartOptions{Profile: p.Profile, Room: rec.ID, Dir: p.Dir, Goal: goal, Task: p.Task}
	try := func(name string) (subagent.Info, error) {
		opts.Name = name
		info, _, err := s.Notify.Sessions.Start(ctx, opts)
		return info, err
	}
	if base == "" {
		return try("")
	}
	for n := 1; n <= maxSuffix; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		info, err := try(name)
		if err == nil || !nameTaken(err) {
			return info, err
		}
	}
	return try("")
}

func nameTaken(err error) bool { return errors.Is(err, subagent.ErrNameTaken) }

// clip keeps at most lines lines and chars characters of text, marking a cut
// with "…".
func clip(text string, lines, chars int) string {
	cut := false
	if ls := strings.Split(text, "\n"); len(ls) > lines {
		text, cut = strings.Join(ls[:lines], "\n"), true
	}
	if r := []rune(text); len(r) > chars {
		text, cut = string(r[:chars]), true
	}
	if cut {
		return strings.TrimRight(text, " \n") + "…"
	}
	return text
}

// SendResult is what one composer send did: Kind is "post" or "spawn".
type SendResult struct {
	Kind  string       `json:"kind"`
	Post  *PostResult  `json:"post,omitempty"`
	Spawn *SpawnResult `json:"spawn,omitempty"`
}

// Send is the room composer's one send path. It parses the draft at send
// time, so a client's debounced preview can never route a spawn to a post:
// a "/<profile> <task>" draft starts a session, "//text" posts "/text",
// and anything else posts as Post does. Files sent with a new task are
// recorded as an owner post before the session starts.
func (s *Service) Send(ctx context.Context, roomRef, draft, replyTo, forwardOf string, atts ...room.Attachment) (SendResult, error) {
	if _, _, ok := splitSpawn(draft); ok {
		if forwardOf != "" {
			return SendResult{}, errors.New("a new session cannot start with a forwarded post. Remove the forwarded post first")
		}
		if replyTo != "" {
			return SendResult{}, errors.New("a new session cannot start as a reply. Cancel the reply first")
		}
		res, err := s.StartSession(ctx, roomRef, draft, atts...)
		if err != nil {
			return SendResult{}, err
		}
		return SendResult{Kind: "spawn", Spawn: &res}, nil
	}
	res, err := s.post(ctx, roomRef, draft, replyTo, forwardOf, atts...)
	return SendResult{Kind: "post", Post: &res}, err
}
