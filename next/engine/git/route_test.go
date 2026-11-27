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
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type fakeAccess struct {
	allowed  bool
	name     string
	recorded map[contract.SessionID][]string // a session's branches, newest first
	holders  map[string]contract.SessionID   // the live session that has a branch
	claimed  *claim
}

// claim is what the last ClaimBranch asked for.
type claim struct {
	wanted      string
	unavailable map[string]bool
}

// routeAccess is session ses_1 on branch work, whose run resumes ses_0, which had branch earlier; ses_2 holds work-taken.
func routeAccess() fakeAccess {
	return fakeAccess{allowed: true, recorded: map[contract.SessionID][]string{"ses_1": {"work"}, "ses_0": {"earlier"}}, holders: map[string]contract.SessionID{"work-taken": "ses_2"}}
}

func (a fakeAccess) SessionBranches(_ context.Context, _ contract.RepoName, session contract.SessionID) ([]string, error) {
	return a.recorded[session], nil
}

func (a fakeAccess) BranchHolder(_ context.Context, _ contract.RepoName, branch string) (contract.SessionID, error) {
	return a.holders[branch], nil
}

func (a fakeAccess) ClaimBranch(_ context.Context, _ contract.RepoName, _ contract.SessionID, wanted string, unavailable map[string]bool, _ time.Time) (string, error) {
	*a.claimed = claim{wanted, unavailable}
	return wanted, nil
}

func (a fakeAccess) RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error) {
	if a.allowed {
		return []contract.RoomRepo{{Repo: "owner/repo"}, {Repo: "owner/other"}}, nil
	}
	return nil, nil
}

func (a fakeAccess) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Repo: "owner/repo", Name: a.name}, nil
}

func (a fakeAccess) OwnsPRBranch(context.Context, contract.RepoName, contract.SessionID, string) (bool, error) {
	return false, nil
}

func (fakeAccess) RecordPushedBranches(context.Context, contract.RepoName, contract.RepoName, contract.SessionID, []string, time.Time) error {
	return nil
}

func defaultMain(context.Context, contract.RepoName, bool) (string, error) { return "main", nil }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRejectsOtherRooms(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	called := false
	transport := roundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", fakeAccess{}, defaultMain).ServeHTTP(w, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
	if w.Code != http.StatusForbidden || called {
		t.Fatalf("status %d, called %v", w.Code, called)
	}
}

type packReader struct {
	header  string
	drained bool
}

func (r *packReader) Read(p []byte) (int, error) {
	if r.header != "" {
		n := copy(p, r.header)
		r.header = r.header[n:]
		return n, nil
	}
	if !r.drained {
		r.drained = true
		return copy(p, "PACK"), nil
	}
	return 0, io.EOF
}

func TestRejectsUnownedPushBeforeUpstream(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	called := false
	transport := roundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/main\x00report-status\n"
	body := &packReader{header: fmt.Sprintf("%04x%s0000", len(line)+4, line)}
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", body))
	if called || !body.drained || !strings.Contains(w.Body.String(), "ng refs/heads/main not owned by this session") {
		t.Fatalf("upstream called %v, response %q", called, w.Body.String())
	}
}

func TestRejectsSidebandPushWithSidebandReport(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/main\x00report-status side-band-64k\n"
	body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
	w := httptest.NewRecorder()
	Handler(target, roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("upstream called"); return nil, nil }), "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	if len(w.Body.Bytes()) < 5 || w.Body.Bytes()[4] != 1 || !strings.Contains(w.Body.String(), "ng refs/heads/main") {
		t.Fatalf("invalid sideband report %q", w.Body.String())
	}
}

func TestForwardsOwnedPushAndReplacesCredential(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/work\x00report-status\n"
	body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		got, err := io.ReadAll(r.Body)
		user, pass, ok := r.BasicAuth()
		if err != nil || string(got) != body || !ok || user != "x-access-token" || pass != "secret" || r.URL.Path != "/owner/repo.git/git-receive-pack" {
			t.Fatalf("bad forwarded request: %q %s %v", got, r.URL.Path, err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	req := httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body))
	req.Header.Set("Authorization", "bad")
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestTopicBranchPushAuthorization(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	for _, tc := range []struct {
		branch  string
		allowed bool
	}{{"work", true}, {"work-topic", true}, {"other", false}, {"work/topic", false}, {"work-taken", false}, {"work-", false}, {"work-a..b", false}, {"work-a.lock", false}} {
		t.Run(tc.branch, func(t *testing.T) {
			called := false
			transport := roundTrip(func(*http.Request) (*http.Response, error) {
				called = true
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})
			line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/" + tc.branch + "\x00report-status\n"
			body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
			w := httptest.NewRecorder()
			Handler(target, transport, "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
			if called != tc.allowed {
				t.Fatalf("called %v, response %s", called, w.Body.String())
			}
		})
	}
}

func TestForwardsDeletionOfOwnedStepBranch(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	line := strings.Repeat("1", 40) + " " + strings.Repeat("0", 40) + " refs/heads/work-0123456789ab\x00report-status\n"
	body := fmt.Sprintf("%04x%s0000", len(line)+4, line)
	called := false
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		called = true
		if got, err := io.ReadAll(r.Body); err != nil || string(got) != body {
			t.Fatalf("bad forwarded body: %q %v", got, err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	if !called || w.Code != 200 {
		t.Fatalf("forwarded %v, status %d: %s", called, w.Code, w.Body.String())
	}
}

// Close keeps no archive ref, so not even a session's own refs/dyna/archive name is pushed upstream.
func TestRefusesAPushToAnArchiveRef(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	called := false
	transport := roundTrip(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/dyna/archive/ses_1\x00report-status\n"
	body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	if called {
		t.Fatalf("the push went upstream: %s", w.Body.String())
	}
}

// A push from a shallow clone sends "shallow <oid>" lines before its commands (sunrise-org/sunrise, 2026-11-20).
func TestForwardsShallowClonePush(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	shallow := "shallow " + strings.Repeat("4", 40)
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/work\x00report-status\n"
	body := fmt.Sprintf("%04x%s%04x%s0000PACK", len(shallow)+4, shallow, len(line)+4, line)
	transport := roundTrip(func(r *http.Request) (*http.Response, error) {
		if got, err := io.ReadAll(r.Body); err != nil || string(got) != body {
			t.Fatalf("bad forwarded body: %q %v", got, err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	// A shallow line after a command is not part of the protocol.
	late := fmt.Sprintf("%04x%s%04x%s0000PACK", len(line)+4, line, len(shallow)+4, shallow)
	w = httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", routeAccess(), defaultMain).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(late)))
	if w.Code != 400 {
		t.Fatalf("late shallow line: status %d", w.Code)
	}
}
