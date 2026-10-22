package watch

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"dyna-agent/internal/pr/state"
)

type NoiseRule = state.NoiseRule

type PRDetails struct {
	Record
	NoiseRules []NoiseRule `json:"noiseRules"`
}

func (s *Service) Show(key string) (PRDetails, error) {
	st, err := s.Store.Read()
	if err != nil {
		return PRDetails{}, err
	}
	r := st.Records[key]
	if r == nil {
		return PRDetails{}, fmt.Errorf("PR is not tracked")
	}
	return PRDetails{Record: *r, NoiseRules: repoNoise(st, refRecord(*r))}, nil
}

func repoNoise(st State, ref Ref) []NoiseRule {
	out := []NoiseRule{}
	for _, r := range st.NoiseRules {
		if strings.EqualFold(r.Host, ref.Host) && strings.EqualFold(r.Repo, ref.Repo) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Service) ListNoise() ([]NoiseRule, error) {
	st, err := s.Store.Read()
	if err != nil {
		return nil, err
	}
	out := []NoiseRule{}
	for _, r := range st.NoiseRules {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// RemoveNoiseInRepos checks the rule's repository under the same lock as
// removal. An empty scope never permits removal.
func (s *Service) RemoveNoiseInRepos(id string, repos []Ref) error {
	if len(repos) == 0 {
		return fmt.Errorf("this room has no GitHub repository scope")
	}
	return s.Store.update(func(st *State) error {
		rule, ok := st.NoiseRules[id]
		if !ok {
			return fmt.Errorf("no learned noise rule %q", id)
		}
		if !slices.ContainsFunc(repos, func(repo Ref) bool {
			return strings.EqualFold(rule.Host, repo.Host) && strings.EqualFold(rule.Repo, repo.Repo)
		}) {
			return fmt.Errorf("noise rule is not in this room's repositories")
		}
		delete(st.NoiseRules, id)
		return nil
	})
}

func isBot(u User) bool {
	return strings.EqualFold(u.Type, "Bot") || strings.HasSuffix(strings.ToLower(u.Login), "[bot]")
}

func findComment(comments []Comment, id string) (Comment, error) {
	kind, raw, qualified := strings.Cut(id, ":")
	if !qualified {
		raw, kind = kind, ""
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return Comment{}, fmt.Errorf("use a numeric comment ID or the kind:id from a PR batch")
	}
	var found []Comment
	for _, c := range comments {
		if c.ID == n && (kind == "" || c.Kind == kind) {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return Comment{}, fmt.Errorf("comment %q was not found on this PR", id)
	}
	if len(found) > 1 {
		return Comment{}, fmt.Errorf("comment ID is ambiguous; use the kind:id from the batch")
	}
	return found[0], nil
}

func (s *Service) LearnNoise(ctx context.Context, key, commentID, reason, learner string) (NoiseRule, bool, error) {
	r, err := s.Store.Get(key)
	if err != nil {
		return NoiseRule{}, false, err
	}
	ref := refRecord(r)
	var pull Pull
	if err = s.github(r.RoomID).api(ctx, ref, ref.path(""), &pull); err != nil {
		return NoiseRule{}, false, err
	}
	comments, err := s.github(r.RoomID).Comments(ctx, ref)
	if err != nil {
		return NoiseRule{}, false, err
	}
	c, err := findComment(comments, commentID)
	if err != nil {
		return NoiseRule{}, false, err
	}
	st, err := s.Store.Read()
	if err != nil {
		return NoiseRule{}, false, err
	}
	if commentOwner(st.CommentSessions, c) == "" && strings.EqualFold(c.User.Login, pull.User.Login) {
		return NoiseRule{}, false, fmt.Errorf("an unattributed owner comment cannot become a noise rule")
	}
	if !isBot(c.User) || c.User.Login == "" {
		return NoiseRule{}, false, fmt.Errorf("only bot comments can become noise rules; %q is a human login", c.User.Login)
	}
	c.Body = Sanitize(c.Body)
	if protectedFeedback(Event{Kind: c.Kind, Text: c.Body}) {
		return NoiseRule{}, false, fmt.Errorf("comment contains review content or code; keep it instead of learning a noise rule")
	}
	if len(c.Body) > 30000 {
		return NoiseRule{}, false, fmt.Errorf("comment exceeds the 30000-byte noise template limit")
	}
	template := normalizeNoise(c.Body)
	if len(strings.Fields(template)) < 2 || len(strings.ReplaceAll(template, " ", "")) < 12 {
		return NoiseRule{}, false, fmt.Errorf("comment has too little fixed text for a safe noise template")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "Agent marked obvious bot spam"
	}
	rule := NoiseRule{Host: strings.ToLower(ref.Host), Repo: strings.ToLower(ref.Repo), Bot: strings.ToLower(c.User.Login), Template: template, Reason: Sanitize(clip(reason, 1000)), SourceURL: c.URL, SourceComment: commentKey(c), LearnedBy: learner, Created: s.now()}
	rule.ID = "noise_" + digest(rule.Host + "/" + rule.Repo + "/" + rule.Bot + "/" + template)[:16]
	created := false
	err = s.Store.update(func(st *State) error {
		if old, ok := st.NoiseRules[rule.ID]; ok {
			rule = old
			return nil
		}
		if len(repoNoise(*st, ref)) >= 256 {
			return fmt.Errorf("this repo has 256 learned rules; remove an unused rule first")
		}
		st.NoiseRules[rule.ID] = rule
		created = true
		return nil
	})
	return rule, created, err
}

var noiseMarkup = regexp.MustCompile(`(?s)<!--.*?-->|<[^>]*>`)
var noiseImage = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
var noiseLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
var noiseURL = regexp.MustCompile(`(?i)\b(?:https?://|www\.)[^\s<>()]+`)
var noiseHandle = regexp.MustCompile(`@[A-Za-z0-9_][A-Za-z0-9_/-]*`)
var noiseISODate = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b`)
var noiseNamedDate = regexp.MustCompile(`(?i)\b(?:(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:tember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)\s+\d{1,2}(?:st|nd|rd|th)?(?:,?\s+\d{4})?|\d{1,2}\s+(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|jun(?:e)?|jul(?:y)?|aug(?:ust)?|sep(?:tember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)(?:\s+\d{4})?)\b`)

func normalizeNoise(s string) string {
	s = html.UnescapeString(s)
	s = noiseMarkup.ReplaceAllString(s, " ")
	s = noiseImage.ReplaceAllString(s, " ")
	s = noiseLink.ReplaceAllString(s, "$1")
	s = noiseURL.ReplaceAllString(s, " ")
	s = noiseHandle.ReplaceAllString(s, " ")
	// Review outcome values carry meaning. Keep their numbers even when an
	// unfamiliar structured layout escapes the positive-count guard below.
	// Retaining extra numbers in these comments favors a keep over a false drop.
	preserveOutcomeNumbers := reviewOutcomeLabel.MatchString(s)
	if !preserveOutcomeNumbers {
		s = noiseISODate.ReplaceAllString(s, " ")
		s = noiseNamedDate.ReplaceAllString(s, " ")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || (preserveOutcomeNumbers && unicode.IsDigit(r)) {
			return unicode.ToLower(r)
		}
		return ' '
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

var reviewContent = regexp.MustCompile(`(?i)\b(?:p[0-3]|bug|regression|incorrect|unsafe|vulnerabilit\w*|severity|critical|major|confidence|should|must|consider|fix|ensure|out.of.bounds|nil.pointer|race.condition|actionable.comments?|findings?|can\s+(?:cause|fail|leak)|will\s+fail)\b`)
var codeLocation = regexp.MustCompile(`(?i)\b(?:line\s+\d+|[A-Za-z0-9_./-]+\.(?:go|py|tsx?|jsx?|rs|java|c|cpp|h|rb|sh|ya?ml|json):\d+)\b`)

// Labels can be plain prose, JSON keys, markdown tables or HTML definition
// lists. Accept common snake_case and camelCase count keys as well.
const reviewOutcomeName = `(?:(?:(?:num(?:ber)?|total|count)[\s_-]*(?:of[\s_-]*)?)?(?:issues?|findings?|bugs?|vulnerabilit(?:y|ies)|problems?|warnings?|failures?|errors?)(?:[\s_-]*(?:count|total|found|reported|detected))?)`

var reviewOutcomeLabel = regexp.MustCompile(`(?i)\b` + reviewOutcomeName + `\b`)
var reviewCount = regexp.MustCompile(`(?i)(?:\b0*[1-9]\d*[\s\p{P}\p{S}]+` + reviewOutcomeName + `\b|\b` + reviewOutcomeName + `\b[\s\p{P}\p{S}]*0*[1-9]\d*\b)`)
var reviewLink = regexp.MustCompile(`#(?:discussion_r|pullrequestreview-|r\d)`)

func protectedFeedback(e Event) bool {
	return e.Kind == "comments" || e.Session != "" || markerPattern.MatchString(e.Text) || strings.Contains(e.Text, "```") || strings.Contains(e.Text, "~~~") || strings.Contains(strings.ToLower(e.Text), "<code") || strings.Contains(strings.ToLower(e.Text), "<pre") || codeLocation.MatchString(e.Text) || reviewContent.MatchString(e.Text) || reviewCount.MatchString(noiseMarkup.ReplaceAllString(html.UnescapeString(e.Text), " ")) || reviewLink.MatchString(e.Text)
}

func learnedDecision(pr PRContext, e Event, rules []NoiseRule) (Decision, bool) {
	if !e.IsBot || protectedFeedback(e) {
		return Decision{}, false
	}
	u, err := url.Parse(pr.URL)
	if err != nil {
		return Decision{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return Decision{}, false
	}
	template := ""
	for _, r := range rules {
		if !strings.EqualFold(r.Bot, e.Commenter) || !strings.EqualFold(r.Host, u.Host) || !strings.EqualFold(r.Repo, parts[0]+"/"+parts[1]) {
			continue
		}
		if template == "" {
			template = normalizeNoise(e.Text)
		}
		// Exact normalized equality is 100% similarity. A substring or a
		// fuzzy match can hide a real finding appended to a long status post.
		if template != "" && template == r.Template {
			return Decision{Action: "drop", Reason: "learned_noise:" + r.ID}, true
		}
	}
	return Decision{}, false
}

// dropSummary returns the filter line, or "" when nothing was dropped.
func dropSummary(counts map[string]int) string {
	total := 0
	var keys []string
	for key, count := range counts {
		if count > 0 {
			total += count
			keys = append(keys, key)
		}
	}
	if total == 0 {
		return ""
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s: %d", key, counts[key]))
	}
	return fmt.Sprintf("Comment filter: dropped %d comments (%s). Learned rules: dyna git pr noise list. Remove a wrong rule: dyna git pr noise rm <id>.", total, strings.Join(parts, "; "))
}
