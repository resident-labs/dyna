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
	"unicode/utf8"
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
	want := "GitHub returned 500 (a transient error on its side; a ref may or may not have been updated, and git ls-remote shows the remote's state); the engine retried once and it failed again"
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
			if len(script.bodies) != 1 || !strings.Contains(w.Body.String(), "GitHub returned 502 (a transient error on its side; a ref may or may not have been updated, and git ls-remote shows the remote's state); the request body was over 1 MiB or streamed, so the engine did not keep it to retry") {
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

func fetchRequest() *http.Request {
	return httptest.NewRequest("GET", "/git/owner/repo.git/info/refs?service=git-upload-pack", nil)
}

func TestLogLineIsCutAtExactly200BytesOnARuneBoundary(t *testing.T) {
	logs := captureLog(t)
	push(t, &upstreamScript{replies: []func() *http.Response{reply(500, strings.Repeat("e", 5000)), reply(500, "x")}}, fetchRequest())
	if !strings.Contains(logs.String(), `error="`+strings.Repeat("e", 200)+`…"`) && !strings.Contains(logs.String(), "error="+strings.Repeat("e", 200)+"…") {
		t.Fatalf("log is not cut at 200 bytes: %s", logs.String())
	}
	cut := captureLog(t)
	push(t, &upstreamScript{replies: []func() *http.Response{reply(500, "a"+strings.Repeat("é", 150)), reply(500, "x")}}, fetchRequest())
	if strings.Contains(cut.String(), `\x`) || !utf8.ValidString(cut.String()) {
		t.Fatalf("the cut split a character: %q", cut.String())
	}
}

type endlessBody struct{ read *int }

func (b endlessBody) Read(p []byte) (int, error) {
	if *b.read > 64<<20 {
		return 0, io.EOF
	}
	*b.read += len(p)
	return len(p), nil
}

func TestA5xxBodyIsReadOnlyUpToTheBound(t *testing.T) {
	read := 0
	endless := func() *http.Response {
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(endlessBody{&read})}
	}
	push(t, &upstreamScript{replies: []func() *http.Response{endless, endless}}, fetchRequest())
	if read > 2*(4<<10+8192) {
		t.Fatalf("read %d bytes of 5xx bodies", read)
	}
}

func TestFirstLineEndsAtAnyControlCharacter(t *testing.T) {
	for name, body := range map[string]string{"carriage return": "first\rsecond", "escape": "first\x1b[31msecond", "delete": "first\x7fsecond", "newline": "first\nsecond"} {
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			push(t, &upstreamScript{replies: []func() *http.Response{reply(500, body), reply(500, body)}}, fetchRequest())
			if !strings.Contains(logs.String(), "first") || strings.Contains(logs.String(), "second") {
				t.Fatalf("log: %s", logs.String())
			}
		})
	}
}

func TestAFetch200HoldingTheCommitRefsTextIsNotRetried(t *testing.T) {
	script := &upstreamScript{replies: []func() *http.Response{reply(200, "remote: fatal error in commit_refs"), reply(200, "again")}}
	w := push(t, script, fetchRequest())
	if len(script.bodies) != 1 || w.Body.String() != "remote: fatal error in commit_refs" {
		t.Fatalf("calls %d, body %q", len(script.bodies), w.Body.String())
	}
}

func TestPushWithoutSidebandGetsNoExtraLineForCommitRefs(t *testing.T) {
	text := "fatal error in commit_refs"
	script := &upstreamScript{replies: []func() *http.Response{reply(200, text)}}
	w := push(t, script, httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(pushBody(""))))
	if len(script.bodies) != 2 || w.Body.String() != text {
		t.Fatalf("calls %d, body %q", len(script.bodies), w.Body.String())
	}
}

func TestAnEmpty5xxBodyLogsTheStatusText(t *testing.T) {
	logs := captureLog(t)
	push(t, &upstreamScript{replies: []func() *http.Response{reply(500, ""), reply(200, "ok")}}, fetchRequest())
	if !strings.Contains(logs.String(), "Internal Server Error") {
		t.Fatalf("log: %s", logs.String())
	}
}

func TestLogNamesTheAttemptAndWhetherItRetries(t *testing.T) {
	logs := captureLog(t)
	push(t, &upstreamScript{replies: []func() *http.Response{reply(500, "a"), reply(500, "b")}}, fetchRequest())
	if !strings.Contains(logs.String(), "attempt=1 retrying=true") || !strings.Contains(logs.String(), "attempt=2 retrying=false") {
		t.Fatalf("log: %s", logs.String())
	}
	streamed := captureLog(t)
	req := httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", io.NopCloser(strings.NewReader(pushBody(""))))
	req.ContentLength = -1
	push(t, &upstreamScript{replies: []func() *http.Response{reply(500, "a")}}, req)
	if !strings.Contains(streamed.String(), "attempt=1 retrying=false") {
		t.Fatalf("log: %s", streamed.String())
	}
}

func TestSyntheticResponsesCarryTheirOwnHeaders(t *testing.T) {
	target, _ := url.Parse("https://github.com")
	failing := func() *http.Response {
		header := make(http.Header)
		header.Set("Content-Length", "4")
		header.Set("Content-Type", "text/html")
		return &http.Response{StatusCode: 500, ContentLength: 4, Header: header, Body: io.NopCloser(strings.NewReader("boom"))}
	}
	server := httptest.NewServer(Handler(target, &upstreamScript{replies: []func() *http.Response{failing}}, "secret-token", "ses_1", "room", routeAccess()))
	defer server.Close()
	resp, err := http.Post(server.URL+"/git/owner/repo.git/git-receive-pack", "application/x-git-receive-pack-request", strings.NewReader(pushBody("")))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-git-receive-pack-result" || !strings.Contains(string(got), "unpack GitHub returned 500") {
		t.Fatalf("status %d, type %q, body %q, err %v", resp.StatusCode, resp.Header.Get("Content-Type"), got, err)
	}
	fetch, err := http.Get(server.URL + "/git/owner/repo.git/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatal(err)
	}
	defer fetch.Body.Close()
	got, err = io.ReadAll(fetch.Body)
	if err != nil || fetch.StatusCode != 500 || fetch.Header.Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(string(got), "GitHub returned 500") {
		t.Fatalf("status %d, type %q, body %q, err %v", fetch.StatusCode, fetch.Header.Get("Content-Type"), got, err)
	}
}

func TestABodyLongerThanItsContentLengthIsNotKeptForRetry(t *testing.T) {
	full := pushBody("") + strings.Repeat("x", replayLimit+10)
	req := httptest.NewRequest("POST", "/git/owner/repo.git/git-receive-pack", strings.NewReader(full))
	req.ContentLength = 100
	script := &upstreamScript{replies: []func() *http.Response{reply(502, "bad"), reply(200, "ok")}}
	push(t, script, req)
	if len(script.bodies) != 1 || script.bodies[0] != full {
		t.Fatalf("calls %d, first body %d bytes of %d", len(script.bodies), len(script.bodies[0]), len(full))
	}
}
