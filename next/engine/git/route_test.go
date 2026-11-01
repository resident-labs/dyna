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
	"github.com/resident-labs/dyna/engine/store"
)

type fakeAccess struct {
	allowed bool
}

func (a fakeAccess) RoomRepos(context.Context, contract.RoomID) ([]contract.RoomRepo, error) {
	if a.allowed {
		return []contract.RoomRepo{{Repo: "owner/repo"}, {Repo: "owner/other"}}, nil
	}
	return nil, nil
}

func (a fakeAccess) Session(context.Context, contract.SessionID) (store.Session, error) {
	return store.Session{Repo: "owner/repo"}, nil
}

func (a fakeAccess) OwnsPRBranch(context.Context, contract.RepoName, contract.SessionID, string) (bool, error) {
	return false, nil
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOtherRoomRepositoryAllowsFetchButRejectsPush(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	called := 0
	transport := roundTrip(func(*http.Request) (*http.Response, error) {
		called++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})
	handler := Handler(target, transport, "secret", "ses_1", "room", fakeAccess{allowed: true})
	fetch := httptest.NewRecorder()
	handler.ServeHTTP(fetch, httptest.NewRequest("GET", "/git/owner/other.git/info/refs?service=git-upload-pack", nil))
	advertise := httptest.NewRecorder()
	handler.ServeHTTP(advertise, httptest.NewRequest("GET", "/git/owner/other.git/info/refs?service=git-receive-pack", nil))
	body := &packReader{header: "pack"}
	push := httptest.NewRecorder()
	handler.ServeHTTP(push, httptest.NewRequest("POST", "/git/owner/other.git/git-receive-pack", body))
	if fetch.Code != 200 || advertise.Code != http.StatusForbidden || push.Code != http.StatusForbidden || !body.drained || called != 1 {
		t.Fatalf("fetch %d, advertisement %d, push %d, drained %v, calls %d", fetch.Code, advertise.Code, push.Code, body.drained, called)
	}
}

func TestRejectsOtherRooms(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	called := false
	transport := roundTrip(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	w := httptest.NewRecorder()
	Handler(target, transport, "secret", "ses_1", "room", fakeAccess{}).ServeHTTP(w, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
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
	Handler(target, transport, "secret", "ses_1", "room", fakeAccess{allowed: true}).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", body))
	if called || !body.drained || !strings.Contains(w.Body.String(), "ng refs/heads/main not owned by this session") {
		t.Fatalf("upstream called %v, response %q", called, w.Body.String())
	}
}

func TestRejectsSidebandPushWithSidebandReport(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/main\x00report-status side-band-64k\n"
	body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
	w := httptest.NewRecorder()
	Handler(target, roundTrip(func(*http.Request) (*http.Response, error) { t.Fatal("upstream called"); return nil, nil }), "secret", "ses_1", "room", fakeAccess{allowed: true}).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	if len(w.Body.Bytes()) < 5 || w.Body.Bytes()[4] != 1 || !strings.Contains(w.Body.String(), "ng refs/heads/main") {
		t.Fatalf("invalid sideband report %q", w.Body.String())
	}
}

func TestForwardsOwnedPushAndReplacesCredential(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/v2/ses_1\x00report-status\n"
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
	Handler(target, transport, "secret", "ses_1", "room", fakeAccess{allowed: true}).ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestTopicBranchPushAuthorization(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	for _, tc := range []struct {
		branch  string
		allowed bool
	}{{"v2/ses_1-topic", true}, {"v2/ses_2", false}, {"v2/ses_1/topic", false}} {
		t.Run(tc.branch, func(t *testing.T) {
			called := false
			transport := roundTrip(func(*http.Request) (*http.Response, error) {
				called = true
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})
			line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/" + tc.branch + "\x00report-status\n"
			body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
			w := httptest.NewRecorder()
			Handler(target, transport, "secret", "ses_1", "room", fakeAccess{allowed: true}).ServeHTTP(w, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
			if called != tc.allowed {
				t.Fatalf("called %v, response %s", called, w.Body.String())
			}
		})
	}
}

func TestNotifyPushNamesTheRepositoryOfAPush(t *testing.T) {
	var pushed []contract.RepoName
	route := NotifyPush(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), func(repo contract.RepoName) { pushed = append(pushed, repo) })
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/git/owner/repo.git/info/refs?service=git-receive-pack", nil),
		httptest.NewRequest(http.MethodPost, "/git/owner/repo.git/git-upload-pack", nil),
		httptest.NewRequest(http.MethodPost, "/git/owner/repo.git/git-receive-pack", nil),
	} {
		route.ServeHTTP(httptest.NewRecorder(), request)
	}
	if len(pushed) != 1 || pushed[0] != "owner/repo" {
		t.Fatalf("pushed %v", pushed)
	}
}
