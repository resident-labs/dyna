package git

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func (fakeAccess) ResumedSessions(context.Context, contract.RepoName, contract.SessionID) ([]contract.SessionID, error) {
	return nil, nil
}

// resumedAccess is a session whose run resumes the run of ses_0.
type resumedAccess struct{ fakeAccess }

func (resumedAccess) ResumedSessions(context.Context, contract.RepoName, contract.SessionID) ([]contract.SessionID, error) {
	return []contract.SessionID{"ses_0"}, nil
}

func pushTo(t *testing.T, rooms RoomRepositories, branch string) (string, bool) {
	t.Helper()
	target, _ := url.Parse("https://github.com")
	forwarded := false
	transport := roundTrip(func(*http.Request) (*http.Response, error) {
		forwarded = true
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/" + branch + "\x00report-status\n"
	body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", rooms).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	return w.Body.String(), forwarded
}

func TestAResumedSessionPushesTheBranchesOfTheRunItResumes(t *testing.T) {
	access := resumedAccess{routeAccess()}
	for _, branch := range []string{"earlier", "earlier-0123456789ab", "earlier-run"} {
		if report, forwarded := pushTo(t, access, branch); !forwarded {
			t.Errorf("%s: %q", branch, report)
		}
	}
	for _, branch := range []string{"other-0123456789ab", "earlier/topic", "main"} {
		if report, forwarded := pushTo(t, access, branch); forwarded || !strings.Contains(report, "ng refs/heads/"+branch) {
			t.Errorf("%s: forwarded %v, %q", branch, forwarded, report)
		}
	}
	if report, forwarded := pushTo(t, routeAccess(), "earlier-0123456789ab"); forwarded || !strings.Contains(report, "not owned by this session") {
		t.Errorf("a session that resumes nothing: forwarded %v, %q", forwarded, report)
	}
}

func TestARefusedPushSaysTheRule(t *testing.T) {
	report, _ := pushTo(t, routeAccess(), "earlier-0123456789ab")
	for _, want := range []string{"not owned by this session", "work,", "work-<topic>", "a workflow run it resumes", "a PR it or a session above it owns"} {
		if !strings.Contains(report, want) {
			t.Errorf("the refusal does not say %q: %q", want, report)
		}
	}
}

func TestAResumedSessionPushesAnEarlierRunsBranchWhileThatRunIsLive(t *testing.T) {
	access := resumedAccess{routeAccess()}
	access.holders = map[string]contract.SessionID{"earlier": "ses_0", "earlier-0123456789ab": "ses_0"}
	for _, branch := range []string{"earlier", "earlier-0123456789ab"} {
		if report, forwarded := pushTo(t, access, branch); !forwarded {
			t.Errorf("%s: %q", branch, report)
		}
	}
}

func TestASessionWithNoBranchYetIsToldSo(t *testing.T) {
	access := routeAccess()
	access.recorded = nil
	report, forwarded := pushTo(t, access, "anything")
	if forwarded || !strings.Contains(report, "it has no branch yet, and can push the branch of a PR it or a session above it owns") {
		t.Fatalf("forwarded %v, %q", forwarded, report)
	}
}
