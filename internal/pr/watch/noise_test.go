package watch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

const quotaNotice = "<p>Review quota exhausted for @alice. Try again in 30 minutes on 2026-10-17. <a href=\"https://bot.example/status/123\">View status</a>.</p>"
const quotaVariant = "Review **quota** exhausted for @bob. Try again in 90 minutes on October 18, 2026. [View status](https://bot.example/status/456)."

func TestRemoveNoiseInReposChecksHostAndRepository(t *testing.T) {
	s := &Service{Store: &Store{Root: t.TempDir()}}
	if err := s.Store.update(func(st *State) error {
		st.NoiseRules["rule"] = NoiseRule{ID: "rule", Host: "github.com", Repo: "owner/repo"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, repos := range [][]Ref{nil, {}, {{Host: "other.test", Repo: "owner/repo"}}, {{Host: "github.com", Repo: "other/repo"}}} {
		if err := s.RemoveNoiseInRepos("rule", repos); err == nil {
			t.Fatalf("accepted scope %+v", repos)
		}
	}
	if rules, err := s.ListNoise(); err != nil || len(rules) != 1 {
		t.Fatalf("rejected removals changed rules: %+v %v", rules, err)
	}
	if err := s.RemoveNoiseInRepos("rule", []Ref{{Host: "GITHUB.COM", Repo: "Owner/Repo"}}); err != nil {
		t.Fatal(err)
	}
	if rules, err := s.ListNoise(); err != nil || len(rules) != 0 {
		t.Fatalf("scoped removal failed: %+v %v", rules, err)
	}
}

func TestLearnedNoiseBatchesCountsAndPreservesFindings(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Checks, e.f.Runs = nil, nil
	r := e.track()
	e.addTrustedComment(101, "issue-comments", quotaNotice, "reviewer-bot[bot]")
	e.write()
	rule, created, err := e.s.LearnNoise(context.Background(), r.Key, "issue-comments:101", "quota notice", e.author.ID)
	if err != nil || !created || rule.Bot != "reviewer-bot[bot]" || rule.Repo != "owner/repo" || rule.Template == "" {
		t.Fatalf("learn: %+v %v %v", rule, created, err)
	}
	if again, created, err := e.s.LearnNoise(context.Background(), r.Key, "101", "another reason", "other-agent"); err != nil || created || again.ID != rule.ID || again.Reason != "quota notice" {
		t.Fatalf("idempotent learning: %+v %v %v", again, created, err)
	}
	e.tick()
	if len(e.sent) != 0 || e.record().PendingDrops["learned_noise:"+rule.ID] != 1 {
		t.Fatalf("noise woke author or lost audit count: %+v %+v", e.sent, e.record())
	}
	e.addTrustedComment(102, "issue-comments", quotaVariant, "reviewer-bot[bot]")
	e.addTrustedComment(103, "issue-comments", "Please inspect the parser.", "owner")
	e.addTrustedComment(104, "issue-comments", quotaVariant+"\nThe return value disappears here.", "reviewer-bot[bot]")
	e.addTrustedComment(105, "issue-comments", "Rate limit exceeded", "coderabbitai[bot]")
	e.f.Log = "last job diagnostic"
	e.setCI("failure", 1)
	e.tick()
	if len(e.sent) != 1 || !strings.Contains(e.sent[0].Text, "The return value disappears here") || !strings.Contains(e.sent[0].Text, "Please inspect") || !strings.Contains(e.sent[0].Text, "comment issue-comments:104") || !strings.Contains(e.sent[0].Text, "dropped 3 comments") || !strings.Contains(e.sent[0].Text, "learned_noise:"+rule.ID+": 2") || !strings.Contains(e.sent[0].Text, "known_rate_limit_noise: 1") {
		t.Fatalf("batched filter audit: %+v", e.sent)
	}
	text := e.sent[0].Text
	if strings.Index(text, "The return value disappears") > strings.Index(text, "Log tail:") || strings.Index(text, "Comment filter:") > strings.Index(text, "Log tail:") {
		t.Fatalf("comments or audit count folded inside the log tail: %s", text)
	}
	if len(e.record().PendingDrops) != 0 {
		t.Fatal("delivered drop counts were not cleared")
	}
	details, err := e.s.Show(r.Key)
	if err != nil || len(details.NoiseRules) != 1 || details.NoiseRules[0].Hits != 2 || details.NoiseRules[0].LastMatched.IsZero() {
		t.Fatalf("show learned rules: %+v %v", details.NoiseRules, err)
	}
	e.s.GH.Executable = "/noise-list-and-rm-do-not-use-github"
	if rules, err := e.s.ListNoise(); err != nil || len(rules) != 1 {
		t.Fatalf("list: %+v %v", rules, err)
	}
	if err := e.s.RemoveNoiseInRepos(rule.ID, []Ref{{Host: "github.com", Repo: r.Repo}}); err != nil {
		t.Fatal(err)
	}
	if rules, err := e.s.ListNoise(); err != nil || len(rules) != 0 {
		t.Fatalf("remove: %+v %v", rules, err)
	}
	if err := e.s.RemoveNoiseInRepos(rule.ID, []Ref{{Host: "github.com", Repo: r.Repo}}); err == nil {
		t.Fatal("removing an unknown rule succeeded")
	}
}

func TestNoiseNormalizationAndWholeTemplateScope(t *testing.T) {
	template := normalizeNoise(quotaNotice)
	if got := normalizeNoise(quotaVariant); got != template || strings.Contains(got, "alice") || strings.Contains(got, "2026") || strings.Contains(got, "http") {
		t.Fatalf("normalized variant: %q != %q", got, template)
	}
	rule := NoiseRule{ID: "noise_test", Host: "github.com", Repo: "owner/repo", Bot: "reviewer-bot[bot]", Template: template}
	for _, tc := range []struct {
		name, url, bot, kind, body, want string
		isBot                            bool
	}{
		{"variant", "https://github.com/owner/repo/pull/17", rule.Bot, "issue-comments", quotaVariant, "drop", true},
		{"another PR same repo", "https://github.com/owner/repo/pull/18", rule.Bot, "issue-comments", quotaVariant, "drop", true},
		{"repo scope", "https://github.com/another/repo/pull/17", rule.Bot, "issue-comments", quotaVariant, "keep", true},
		{"host scope", "https://git.example/owner/repo/pull/17", rule.Bot, "issue-comments", quotaVariant, "keep", true},
		{"bot scope", "https://github.com/owner/repo/pull/17", "other[bot]", "issue-comments", quotaVariant, "keep", true},
		{"human", "https://github.com/owner/repo/pull/17", rule.Bot, "issue-comments", quotaVariant, "keep", false},
		{"inline", "https://github.com/owner/repo/pull/17", rule.Bot, "comments", quotaVariant, "keep", true},
		{"appended content", "https://github.com/owner/repo/pull/17", rule.Bot, "issue-comments", quotaVariant + "\nThe return value disappears here.", "keep", true},
		{"changed wording", "https://github.com/owner/repo/pull/17", rule.Bot, "issue-comments", "Review quota restored; results are ready.", "keep", true},
		{"marked feedback", "https://github.com/owner/repo/pull/17", rule.Bot, "issue-comments", quotaVariant + Marker("ses_reviewer"), "drop", true},
		{"new review link", "https://github.com/owner/repo/pull/17", rule.Bot, "issue-comments", quotaVariant + " https://github.com/owner/repo/pull/17#discussion_r123", "keep", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := (Rules{Learned: []NoiseRule{rule}}).Triage(PRContext{URL: tc.url, OwnerLogin: "owner"}, []Event{{Kind: tc.kind, Association: "COLLABORATOR", Commenter: tc.bot, IsBot: tc.isBot, Text: tc.body}})[0]
			if d.Action != tc.want {
				t.Fatalf("%+v, want %s", d, tc.want)
			}
		})
	}
	// An unmarked owner always wins, even over a bad rule loaded from disk.
	rule.Bot = "owner"
	d := (Rules{Learned: []NoiseRule{rule}}).Triage(PRContext{URL: "https://github.com/owner/repo/pull/17", OwnerLogin: "owner"}, []Event{{Association: "COLLABORATOR", Commenter: "owner", IsBot: true, Text: quotaVariant}})[0]
	if d.Action != "keep" || d.Reason != "owner" {
		t.Fatalf("owner overridden by learned rule: %+v", d)
	}
}

func TestNoiseLearningRefusesHumanOwnerAndReviewContent(t *testing.T) {
	e := newEnv(t)
	r := e.track()
	for i, tc := range []struct{ name, login, kind, body string }{
		{"human", "human", "issue-comments", quotaNotice},
		{"owner", "owner", "issue-comments", quotaNotice},
		{"inline", "reviewer-bot[bot]", "comments", quotaNotice},
		{"code", "reviewer-bot[bot]", "issue-comments", "```go\nreturn nil\n```"},
		{"finding", "reviewer-bot[bot]", "issue-comments", "The parser should reject a negative length."},
		{"confidence", "reviewer-bot[bot]", "issue-comments", "Confidence score: 2/5. This change has several risks."},
		{"too short", "reviewer-bot[bot]", "issue-comments", "@user 123 https://bot.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := int64(i + 1)
			e.addTrustedComment(id, tc.kind, tc.body, tc.login)
			if tc.name == "owner" {
				e.f.Comments[len(e.f.Comments)-1].User.Type = "Bot"
			}
			e.write()
			if _, _, err := e.s.LearnNoise(context.Background(), r.Key, commentKey(e.f.Comments[len(e.f.Comments)-1]), "spam", e.author.ID); err == nil {
				t.Fatal("learned protected feedback")
			}
		})
	}
	if rules, _ := e.s.ListNoise(); len(rules) != 0 {
		t.Fatalf("refused rules were stored: %+v", rules)
	}
}

func TestReviewOutcomeNumbersCannotBecomeNoise(t *testing.T) {
	rule := NoiseRule{ID: "noise_zero", Host: "github.com", Repo: "owner/repo", Bot: "reviewer-bot[bot]", Template: normalizeNoise("Review complete with 0 issues.")}
	d := (Rules{Learned: []NoiseRule{rule}}).Triage(PRContext{URL: "https://github.com/owner/repo/pull/17"}, []Event{{Kind: "reviews", Association: "COLLABORATOR", Commenter: rule.Bot, IsBot: true, Text: "Review complete with 2 issues."}})[0]
	if d.Action != "keep" {
		t.Fatalf("normalization hid a real issue count: %+v", d)
	}
}

func TestBuiltInWholeNoiseTemplatesKeepAddedFindings(t *testing.T) {
	for _, rule := range builtinNoise {
		t.Run(rule.id, func(t *testing.T) {
			for _, tc := range []struct{ text, want string }{{rule.text, "drop"}, {rule.text + " The return value disappears here.", "keep"}} {
				d := (Rules{}).Triage(PRContext{}, []Event{{Association: "COLLABORATOR", Commenter: rule.bot, IsBot: true, Text: tc.text}})[0]
				if d.Action != tc.want {
					t.Fatalf("%+v, want %s", d, tc.want)
				}
			}
		})
	}
	for _, state := range []string{"COMMENTED", "APPROVED", "CHANGES_REQUESTED"} {
		d := (Rules{}).Triage(PRContext{}, []Event{{Kind: "reviews", ReviewState: state, IsBot: true, Association: "COLLABORATOR", Commenter: "any[bot]"}})[0]
		if (d.Action == "drop") != (state == "COMMENTED") {
			t.Fatalf("empty %s: %+v", state, d)
		}
	}
}

func TestLearnedZeroOutcomeKeepsStructuredNonzeroCounts(t *testing.T) {
	e := newEnv(t)
	e.authorFeedOnly()
	e.f.Checks, e.f.Runs = nil, nil
	r := e.track()
	formats := []string{
		"Review completed. Issues: %d.",
		"Review completed. {\"issue_count\": %d}",
		"Review completed. {\"issueCount\": %d}",
		"Review completed.\n| Issues | %d |",
		"Review completed. <dl><dt>Issues</dt><dd>%d</dd></dl>",
	}
	for i, format := range formats {
		id := int64(301 + i)
		e.addTrustedComment(id, "issue-comments", fmt.Sprintf(format, 0), "reviewer-bot[bot]")
		e.write()
		if _, _, err := e.s.LearnNoise(context.Background(), r.Key, strconv.FormatInt(id, 10), "zero-issue status", e.author.ID); err != nil {
			t.Fatalf("learn %q: %v", format, err)
		}
	}
	e.tick()
	if len(e.sent) != 0 {
		t.Fatalf("zero outcomes did not match their learned templates: %+v", e.sent)
	}
	var useful []string
	for i, format := range formats {
		for j, count := range []int{2, 10} {
			body := fmt.Sprintf(format, count)
			useful = append(useful, body)
			e.addTrustedComment(int64(401+i*2+j), "issue-comments", body, "reviewer-bot[bot]")
		}
	}
	e.tick()
	if len(e.sent) != 1 {
		t.Fatalf("nonzero outcomes must wake the author in one batch: %+v", e.sent)
	}
	for _, body := range useful {
		if !strings.Contains(e.sent[0].Text, quotedComment(body)) {
			t.Errorf("nonzero outcome missing from author batch: %q", body)
		}
	}
	for i := range useful {
		if _, _, err := e.s.LearnNoise(context.Background(), r.Key, strconv.Itoa(401+i), "not noise", e.author.ID); err == nil {
			t.Errorf("learned a nonzero review outcome: %q", useful[i])
		}
	}
}

func TestOutcomeNormalizationPreservesCountsButNotQuotaNumbers(t *testing.T) {
	for _, format := range []string{
		"Review completed. Issues: %d.", "Review completed. Issues = **%d**.",
		"Review completed. {\"issues\": %d}", "Review completed. {\"issues_found\": %d}",
		"Review completed. {\"totalIssues\": %d}", "Review completed. {\"number_of_issues\": %d}",
		"Review completed. warnings: %d.", "Review completed. errors_count: %d.",
		"Review completed. <b>Problems</b>: %d.", "Review completed. | Failures | %d |",
	} {
		t.Run(format, func(t *testing.T) {
			zero, positive := fmt.Sprintf(format, 0), fmt.Sprintf(format, 10)
			if normalizeNoise(zero) == normalizeNoise(positive) {
				t.Fatalf("normalization erased changed review outcome: %q", positive)
			}
			if !protectedFeedback(Event{Kind: "issue-comments", Text: positive}) {
				t.Fatalf("nonzero review outcome was not protected: %q", positive)
			}
		})
	}
	if normalizeNoise(quotaNotice) != normalizeNoise(quotaVariant) {
		t.Fatal("changing harmless quota numbers stopped matching")
	}
}

func TestNoticeOmitsFilterLineWhenNothingWasDropped(t *testing.T) {
	r := &Record{Key: "github.com/resident-labs/dyna#1", Head: "abc"}
	notice(r, "room", "PR #1 is ready to land.", "ready", map[string]int{})
	if len(r.Notices) != 1 {
		t.Fatalf("notices = %d; want 1", len(r.Notices))
	}
	if strings.Contains(r.Notices[0].Text, "Comment filter") {
		t.Fatalf("notice text = %q; want no filter line when nothing was dropped", r.Notices[0].Text)
	}
}

func TestNoticeKeepsFilterLineWhenCommentsWereDropped(t *testing.T) {
	r := &Record{Key: "github.com/resident-labs/dyna#1", Head: "abc"}
	notice(r, "room", "PR #1 is ready to land.", "ready", map[string]int{"learned": 2})
	if !strings.Contains(r.Notices[0].Text, "Comment filter: dropped 2 comments") {
		t.Fatalf("notice text = %q; want the filter line with the drop count", r.Notices[0].Text)
	}
}
