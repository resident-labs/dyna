package prs

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func pullJSON(t *testing.T, rollup, reviewDecision, comments string) gqlPull {
	t.Helper()
	var pull gqlPull
	raw := fmt.Sprintf(`{"number":1,"state":"OPEN","headRefOid":%q,"reviewDecision":%s,"commits":{"nodes":[{"commit":{"oid":%q,"statusCheckRollup":%s}}]},"comments":{"nodes":[%s]}}`, sha, reviewDecision, sha, rollup, comments)
	if err := json.Unmarshal([]byte(raw), &pull); err != nil {
		t.Fatal(err)
	}
	return pull
}

func TestAnEmptyRollupIsNoChecksNotAPass(t *testing.T) {
	for name, test := range map[string]struct {
		rollup string
		want   contract.PrCI
	}{
		"no rollup":                  {`null`, contract.PrCINone},
		"success with no checks":     {`{"state":"SUCCESS","contexts":{"nodes":[]}}`, contract.PrCINone},
		"success with a check":       {`{"state":"SUCCESS","contexts":{"nodes":[{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"SUCCESS"}]}}`, contract.PrCISuccess},
		"pending with no checks yet": {`{"state":"PENDING","contexts":{"nodes":[]}}`, contract.PrCIPending},
	} {
		t.Run(name, func(t *testing.T) {
			if got := pullJSON(t, test.rollup, "null", "").observed("me", time.Time{}); got.CI != test.want {
				t.Fatalf("CI %q, want %q", got.CI, test.want)
			}
		})
	}
}

func TestARollupOfAnEarlierHeadIsPendingForTheNewOne(t *testing.T) {
	pull := pullJSON(t, `{"state":"SUCCESS","contexts":{"nodes":[{"__typename":"CheckRun","name":"lint","status":"COMPLETED","conclusion":"SUCCESS"}]}}`, "null", "")
	pull.Commits.Nodes[0].Commit.OID = "deadbeef"
	if got := pull.observed("me", time.Time{}); got.CI != contract.PrCIPending {
		t.Fatalf("CI %q for a head whose own checks have not reported", got.CI)
	}
}

func TestAVerdictCommentAtTheHeadFromTheViewerIsTheReview(t *testing.T) {
	comment := func(author, body string) string {
		text, _ := json.Marshal(body)
		return fmt.Sprintf(`{"body":%s,"author":{"login":%q}}`, text, author)
	}
	const disclosure = "> 🤖 Review by Aria's agent swarm, Dyna, not by Aria.\n"
	approved := comment("me", disclosure+"**Verdict: Approved at "+sha[:12]+"**\nfine")
	for name, test := range map[string]struct {
		decision string
		comments string
		want     contract.PrReview
	}{
		"approved at the head":          {`null`, approved, contract.PrApproved},
		"full sha":                      {`null`, comment("me", disclosure+"**Verdict: Approved at "+sha+"**"), contract.PrApproved},
		"approved at an older head":     {`null`, comment("me", disclosure+"**Verdict: Approved at deadbeefcafe**"), ""},
		"someone else's comment":        {`null`, comment("stranger", disclosure+"**Verdict: Approved at "+sha[:12]+"**"), ""},
		"the viewer without the mark":   {`null`, comment("me", "**Verdict: Approved at "+sha[:12]+"**"), ""},
		"too short a sha":               {`null`, comment("me", disclosure+"**Verdict: Approved at 0123**"), ""},
		"changes requested at head":     {`null`, comment("me", disclosure+"**Verdict: Changes requested at "+sha[:12]+"**"), contract.PrChangesRequested},
		"the later verdict wins":        {`null`, approved + "," + comment("me", disclosure+"**Verdict: Changes requested at "+sha[:12]+"**"), contract.PrChangesRequested},
		"review required, then comment": {`"REVIEW_REQUIRED"`, approved, contract.PrApproved},
		"a GitHub request stands":       {`"CHANGES_REQUESTED"`, approved, contract.PrChangesRequested},
		"a GitHub approval stands":      {`"APPROVED"`, "", contract.PrApproved},
	} {
		t.Run(name, func(t *testing.T) {
			if got := pullJSON(t, `null`, test.decision, test.comments).observed("me", time.Time{}); got.Review != test.want {
				t.Fatalf("review %q, want %q", got.Review, test.want)
			}
		})
	}
}

func TestAVerdictInAReviewBodyIsTheReviewToo(t *testing.T) {
	const disclosure = "> 🤖 Review by Aria's agent swarm, Dyna, not by Aria.\n"
	at := func(minute int) string { return fmt.Sprintf("2026-11-03T10:%02d:00Z", minute) }
	comment := func(author, verdict string, minute int) string {
		text, _ := json.Marshal(disclosure + "**Verdict: " + verdict + "**")
		return fmt.Sprintf(`{"body":%s,"author":{"login":%q},"createdAt":%q,"lastEditedAt":null}`, text, author, at(minute))
	}
	review := func(author, state, verdict, submitted, edited string) string {
		text, _ := json.Marshal(disclosure + "**Verdict: " + verdict + "**")
		return fmt.Sprintf(`{"fullDatabaseId":"1","state":%q,"body":%s,"url":"u","author":{"login":%q},"submittedAt":%s,"lastEditedAt":%s}`, state, text, author, submitted, edited)
	}
	quote := func(minute int) string { return `"` + at(minute) + `"` }
	approved, changes := "Approved at "+sha, "Changes requested at "+sha
	for name, test := range map[string]struct {
		comments, reviews string
		want              contract.PrReview
	}{
		"a COMMENTED review at the head":      {"", review("me", "COMMENTED", approved, quote(1), "null"), contract.PrApproved},
		"a review at an old sha":              {"", review("me", "COMMENTED", "Approved at deadbeefcafe", quote(1), "null"), ""},
		"a review from another account":       {"", review("stranger", "COMMENTED", approved, quote(1), "null"), ""},
		"a short sha of 7 characters":         {"", review("me", "COMMENTED", "Approved at "+sha[:7], quote(1), "null"), ""},
		"a pending review":                    {"", review("me", "PENDING", approved, "null", "null"), ""},
		"edited from old sha to the head":     {"", review("me", "COMMENTED", approved, quote(1), quote(5)), contract.PrApproved},
		"edited away from the head":           {"", review("me", "COMMENTED", "Approved at deadbeefcafe", quote(1), quote(5)), ""},
		"a later review beats a comment":      {comment("me", approved, 1), review("me", "COMMENTED", changes, quote(2), "null"), contract.PrChangesRequested},
		"a later comment beats a review":      {comment("me", changes, 3), review("me", "COMMENTED", approved, quote(2), "null"), contract.PrChangesRequested},
		"a later approval beats a request":    {comment("me", changes, 1), review("me", "COMMENTED", approved, quote(2), "null"), contract.PrApproved},
		"an edited review is as late as edit": {comment("me", changes, 3), review("me", "COMMENTED", changes, quote(1), quote(4)) + "," + review("me", "COMMENTED", approved, quote(2), quote(5)), contract.PrApproved},
	} {
		t.Run(name, func(t *testing.T) {
			pull := pullJSON(t, `null`, `null`, test.comments)
			if err := json.Unmarshal([]byte(`{"nodes":[`+test.reviews+`]}`), &pull.Reviews); err != nil {
				t.Fatal(err)
			}
			if got := pull.observed("me", time.Time{}); got.Review != test.want {
				t.Fatalf("review %q, want %q", got.Review, test.want)
			}
		})
	}
	// GitHub's own request for changes stands over a review's approval.
	pull := pullJSON(t, `null`, `"CHANGES_REQUESTED"`, "")
	if err := json.Unmarshal([]byte(`{"nodes":[`+review("me", "COMMENTED", approved, quote(1), "null")+`]}`), &pull.Reviews); err != nil {
		t.Fatal(err)
	}
	if got := pull.observed("me", time.Time{}); got.Review != contract.PrChangesRequested {
		t.Fatalf("review %q over GitHub's request", got.Review)
	}
}
