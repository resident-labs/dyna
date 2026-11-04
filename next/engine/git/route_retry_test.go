package git

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// upstreamScript answers each call with the next response and records the bodies it was sent.
type upstreamScript struct {
	replies []func() *http.Response
	bodies  []string
}

func (s *upstreamScript) RoundTrip(r *http.Request) (*http.Response, error) {
	got := ""
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}
	s.bodies = append(s.bodies, got)
	return s.replies[min(len(s.bodies), len(s.replies))-1](), nil
}

func reply(status int, body string) func() *http.Response {
	return func() *http.Response {
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	}
}

// sidebandCommitRefs is a 200 push answer whose sideband error band holds GitHub's transient error.
func sidebandCommitRefs() string {
	var b bytes.Buffer
	pkt(&b, "\x02remote: fatal error in commit_refs\n")
	b.WriteString("0000")
	return b.String()
}

func pushBody(sideband string) string {
	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/work\x00report-status " + sideband + "\n"
	return fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
}

func captureLog(t *testing.T) *bytes.Buffer {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

func push(t *testing.T, transport http.RoundTripper, req *http.Request) *httptest.ResponseRecorder {
	target, _ := url.Parse("https://github.com")
	w := httptest.NewRecorder()
	Handler(target, transport, "secret-token", "ses_1", "room", routeAccess()).ServeHTTP(w, req)
	return w
}

func TestPushRetriesOnceAfterUpstream5xx(t *testing.T) {
	logs := captureLog(t)
	body := pushBody("")
	script := &upstreamScript{replies: []func() *http.Response{reply(503, "Service Unavailable secret-token\nsecond line"), reply(200, "ok")}}
	w := push(t, script, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(body)))
	if w.Code != 200 || w.Body.String() != "ok" || len(script.bodies) != 2 || script.bodies[0] != body || script.bodies[1] != body {
		t.Fatalf("status %d, body %q, calls %q", w.Code, w.Body.String(), script.bodies)
	}
	line := logs.String()
	for _, want := range []string{"repo=owner/repo", "session=ses_1", "status=503", "Service Unavailable", "[redacted]"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "secret-token") || strings.Contains(line, "second line") {
		t.Fatalf("log leaks: %s", line)
	}
}

func TestPushRetriesOnceAfterCommitRefsInA200(t *testing.T) {
	logs := captureLog(t)
	script := &upstreamScript{replies: []func() *http.Response{reply(200, sidebandCommitRefs()), reply(200, "0000")}}
	w := push(t, script, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(pushBody("side-band-64k"))))
	if w.Body.String() != "0000" || len(script.bodies) != 2 || !strings.Contains(logs.String(), "fatal error in commit_refs") {
		t.Fatalf("body %q, calls %d, log %s", w.Body.String(), len(script.bodies), logs.String())
	}
}

func TestSecondPushFailureIsReturnedWithItsCause(t *testing.T) {
	script := &upstreamScript{replies: []func() *http.Response{reply(500, "boom"), reply(500, "boom"), reply(200, "never")}}
	w := push(t, script, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(pushBody("side-band-64k"))))
	want := "GitHub returned 500 (a transient error on its side; no ref was updated); the engine retried once and it failed again"
	if len(script.bodies) != 2 || w.Code != 200 || !strings.Contains(w.Body.String(), "unpack "+want) || w.Body.Bytes()[4] != 1 {
		t.Fatalf("calls %d, status %d, body %q", len(script.bodies), w.Code, w.Body.String())
	}
	plain := push(t, &upstreamScript{replies: []func() *http.Response{reply(500, "boom")}}, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(pushBody(""))))
	if !strings.HasPrefix(plain.Body.String(), fmt.Sprintf("%04x", len("unpack "+want)+4+1)+"unpack GitHub returned 500") {
		t.Fatalf("plain report %q", plain.Body.String())
	}
}

func TestSecondCommitRefsInA200CarriesTheCause(t *testing.T) {
	script := &upstreamScript{replies: []func() *http.Response{reply(200, sidebandCommitRefs())}}
	w := push(t, script, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(pushBody("side-band-64k"))))
	if len(script.bodies) != 2 || !strings.Contains(w.Body.String(), "\x02GitHub reported \"fatal error in commit_refs\" (a transient error on its side; no ref was updated)") || !strings.Contains(w.Body.String(), "remote: fatal error in commit_refs") {
		t.Fatalf("calls %d, body %q", len(script.bodies), w.Body.String())
	}
}

func TestOversizedOrStreamedPushIsNotRetried(t *testing.T) {
	big := pushBody("") + strings.Repeat("x", replayLimit)
	chunked := httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", io.NopCloser(strings.NewReader(pushBody(""))))
	chunked.ContentLength = -1
	for name, req := range map[string]*http.Request{
		"over the bound": httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(big)),
		"chunked":        chunked,
	} {
		t.Run(name, func(t *testing.T) {
			script := &upstreamScript{replies: []func() *http.Response{reply(502, "bad gateway"), reply(200, "ok")}}
			w := push(t, script, req)
			if len(script.bodies) != 1 || !strings.Contains(w.Body.String(), "GitHub returned 502 (a transient error on its side; no ref was updated); the request body was over 1 MiB or streamed, so the engine did not keep it to retry") {
				t.Fatalf("calls %d, body %q", len(script.bodies), w.Body.String())
			}
		})
	}
}

func TestFetchRetriesOnceOn5xxOnlyAndNeverOn4xx(t *testing.T) {
	script := &upstreamScript{replies: []func() *http.Response{reply(503, "down"), reply(200, "refs")}}
	w := push(t, script, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
	if w.Code != 200 || w.Body.String() != "refs" || len(script.bodies) != 2 {
		t.Fatalf("status %d, body %q, calls %d", w.Code, w.Body.String(), len(script.bodies))
	}
	again := &upstreamScript{replies: []func() *http.Response{reply(503, "down"), reply(504, "down")}}
	w = push(t, again, httptest.NewRequest("POST", "/git/owner/repo.git/git-upload-pack", strings.NewReader("0000")))
	if w.Code != 504 || w.Body.String() != "GitHub returned 504 (a transient error on its side); the engine retried once and it failed again\n" || len(again.bodies) != 2 || again.bodies[1] != "0000" {
		t.Fatalf("status %d, body %q, calls %q", w.Code, w.Body.String(), again.bodies)
	}
	denied := &upstreamScript{replies: []func() *http.Response{reply(403, "no"), reply(200, "ok")}}
	w = push(t, denied, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
	if w.Code != 403 || len(denied.bodies) != 1 {
		t.Fatalf("status %d, calls %d", w.Code, len(denied.bodies))
	}
}

func TestLogLineIsBounded(t *testing.T) {
	logs := captureLog(t)
	push(t, &upstreamScript{replies: []func() *http.Response{reply(500, strings.Repeat("e", 5000)), reply(500, "x")}}, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
	if strings.Contains(logs.String(), strings.Repeat("e", lineLimit+1)) {
		t.Fatalf("log line is not bounded: %d bytes", logs.Len())
	}
}

func TestLogLineHidesCredentialsInURLs(t *testing.T) {
	logs := captureLog(t)
	push(t, &upstreamScript{replies: []func() *http.Response{reply(500, "see https://user:hunter2@github.com/x"), reply(500, "x")}}, httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil))
	if strings.Contains(logs.String(), "hunter2") || !strings.Contains(logs.String(), "://[redacted]@github.com/x") {
		t.Fatalf("log: %s", logs.String())
	}
}
