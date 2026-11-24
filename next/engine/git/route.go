// Package git handles the repository mirror and the box's smart-HTTP route.
package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// Handler binds a smart-HTTP route to a session. The upstream and token belong
// to the engine, not to the box.
//
// Every failed upstream request is logged, and one that GitHub answered with a 5xx (or, for a push, with a commit_refs error)
// is sent once more when its body was kept; see retrier.
func Handler(upstream *url.URL, transport http.RoundTripper, token string, session contract.SessionID, room contract.RoomID, rooms RoomRepositories, defaultBranch func(context.Context, contract.RepoName, bool) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/git/"), "/")
		if !strings.HasPrefix(r.URL.Path, "/git/") || (len(parts) != 3 && len(parts) != 4) || !validPart(parts[0]) || !strings.HasSuffix(parts[1], ".git") || !validPart(strings.TrimSuffix(parts[1], ".git")) {
			http.Error(w, "invalid repository path", http.StatusBadRequest)
			return
		}
		repo := contract.RepoName(parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"))
		if !((r.Method == http.MethodGet && len(parts) == 4 && parts[2] == "info" && parts[3] == "refs" && (r.URL.Query().Get("service") == "git-upload-pack" || r.URL.Query().Get("service") == "git-receive-pack")) || (r.Method == http.MethodPost && len(parts) == 3 && (parts[2] == "git-upload-pack" || parts[2] == "git-receive-pack"))) {
			http.NotFound(w, r)
			return
		}
		repos, err := rooms.RoomRepos(r.Context(), room)
		if err != nil {
			http.Error(w, "repository access unavailable", http.StatusServiceUnavailable)
			return
		}
		if !slices.ContainsFunc(repos, func(item contract.RoomRepo) bool { return item.Repo == repo }) {
			http.Error(w, "repository not in room", http.StatusForbidden)
			return
		}
		var sideband bool
		if r.Method == http.MethodPost && parts[2] == "git-receive-pack" {
			if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
				http.Error(w, "unsupported content encoding", http.StatusUnsupportedMediaType)
				return
			}
			var body io.ReadCloser
			var refs []string
			body, refs, sideband, err = commands(r.Body)
			if err != nil {
				http.Error(w, "invalid receive-pack commands", http.StatusBadRequest)
				return
			}
			ctx := r.Context()
			selected, err := rooms.Session(ctx, session)
			if err != nil {
				http.Error(w, "repository access unavailable", http.StatusServiceUnavailable)
				return
			}
			allowed, err := checkPush(ctx, rooms, defaultBranch, selected.Repo, repo, session, refs)
			if err != nil {
				http.Error(w, "repository access unavailable", http.StatusServiceUnavailable)
				return
			}
			if !allowed {
				if _, err := io.Copy(io.Discard, body); err != nil {
					http.Error(w, "invalid receive-pack body", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
				var report bytes.Buffer
				pkt(&report, "unpack ok\n")
				rule, err := pushRule(ctx, rooms, selected.Repo, repo, session)
				if err != nil {
					http.Error(w, "repository access unavailable", http.StatusServiceUnavailable)
					return
				}
				for _, ref := range refs {
					pkt(&report, "ng "+ref+" "+rule+"\n")
				}
				io.WriteString(&report, "0000")
				frameReport(w, &report, sideband)
				return
			}
			r.Body = body
		}
		next := &retrier{next: transport, token: token, repo: repo, session: session, push: r.Method == http.MethodPost && parts[2] == "git-receive-pack", sideband: sideband}
		next.replayable = r.Method == http.MethodGet || (r.ContentLength >= 0 && r.ContentLength <= replayLimit)
		if r.Method == http.MethodPost && next.replayable {
			if next.replay, err = io.ReadAll(io.LimitReader(r.Body, replayLimit+1)); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(next.replay), r.Body), r.Body}
			next.replayable = len(next.replay) <= replayLimit
		}
		(&httputil.ReverseProxy{
			Transport: next,
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(upstream)
				r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, "/git")
				r.Out.Host = upstream.Host
				r.Out.Header.Del("Authorization")
				r.Out.SetBasicAuth("x-access-token", token)
			},
		}).ServeHTTP(w, r)
	})
}

const (
	// replayLimit is the largest request body the route keeps in memory to send it again: git's own http.postBuffer default,
	// and the same 1 MiB that bounds the command section of a push. A larger or chunked body streams through once.
	replayLimit = 1 << 20
	// reportLimit is how much of a 200 push response the route reads to look for the commit_refs error. A report-status
	// answer is far smaller; a longer one streams on after what was read.
	reportLimit = 64 << 10
	// failureBodyLimit is how much of a 5xx body is read for the log line and then dropped.
	failureBodyLimit = 4 << 10
	// lineLimit bounds the upstream error text in a log line.
	lineLimit = 200

	commitRefsError = "fatal error in commit_refs"
)

var urlCredentials = regexp.MustCompile(`://[^/@\s]*@`)

// retrier is the transport of one request to GitHub. It logs each failed attempt, and sends a request once more after a
// 5xx answer, or after a push answer that holds the commit_refs error, when the request body was kept (replayable). It cannot
// see a failure further into a streamed 200 response, and it does not retry a transport error: the request may have arrived.
type retrier struct {
	next       http.RoundTripper
	token      string
	repo       contract.RepoName
	session    contract.SessionID
	push       bool
	sideband   bool
	replayable bool
	replay     []byte
}

func (u *retrier) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, failed, err := u.once(req, 1)
	if err != nil || !failed {
		return resp, err
	}
	if u.replayable {
		resp.Body.Close()
		again := req.Clone(req.Context())
		again.Body = nil
		if len(u.replay) > 0 {
			again.Body = io.NopCloser(bytes.NewReader(u.replay))
		}
		if resp, failed, err = u.once(again, 2); err != nil || !failed {
			return resp, err
		}
	}
	text := fmt.Sprintf("GitHub returned %d", resp.StatusCode)
	if resp.StatusCode == http.StatusOK {
		text = "GitHub reported " + strconv.Quote(commitRefsError)
	}
	text += " (a transient error on its side"
	if u.push && resp.StatusCode == http.StatusOK {
		text += "; no ref was updated"
	} else if u.push {
		text += "; a ref may or may not have been updated, and git ls-remote shows the remote's state"
	}
	text += ")"
	if u.replayable {
		text += "; the engine retried once and it failed again"
	} else {
		text += "; the request body was over 1 MiB or streamed, so the engine did not keep it to retry"
	}
	resp.Header.Del("Content-Length")
	resp.Header.Del("Content-Encoding")
	resp.ContentLength = -1
	switch {
	case resp.StatusCode == http.StatusOK && u.sideband:
		var message bytes.Buffer
		pkt(&message, "\x02"+text+"\n")
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(&message, resp.Body), resp.Body}
	case resp.StatusCode == http.StatusOK:
	case u.push:
		var report bytes.Buffer
		pkt(&report, "unpack "+text+"\n")
		io.WriteString(&report, "0000")
		var framed bytes.Buffer
		frameReport(&framed, &report, u.sideband)
		resp.StatusCode, resp.Status = http.StatusOK, "200 OK"
		resp.Header.Set("Content-Type", "application/x-git-receive-pack-result")
		resp.Body = io.NopCloser(&framed)
	default:
		resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
		resp.Body = io.NopCloser(strings.NewReader(text + "\n"))
	}
	return resp, nil
}

// once sends req and reports whether GitHub failed it, after logging the failure. A failed response has a body that is
// still readable by the caller (a 200 push answer) or closed (a 5xx).
func (u *retrier) once(req *http.Request, attempt int) (*http.Response, bool, error) {
	resp, err := u.next.RoundTrip(req)
	if err != nil {
		slog.Warn("git route: upstream request failed", "repo", u.repo, "session", u.session, "path", req.URL.Path, "status", 0, "error", u.line([]byte(err.Error())), "attempt", attempt)
		return nil, false, err
	}
	var head []byte
	switch {
	case resp.StatusCode >= 500:
		head, _ = io.ReadAll(io.LimitReader(resp.Body, failureBodyLimit))
		resp.Body.Close()
		if len(head) == 0 {
			head = []byte(http.StatusText(resp.StatusCode))
		}
		resp.Body = io.NopCloser(bytes.NewReader(nil))
	case u.push && resp.StatusCode == http.StatusOK:
		head, _ = io.ReadAll(io.LimitReader(resp.Body, reportLimit))
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
		if !bytes.Contains(head, []byte(commitRefsError)) {
			return resp, false, nil
		}
	default:
		return resp, false, nil
	}
	slog.Warn("git route: upstream request failed", "repo", u.repo, "session", u.session, "path", req.URL.Path, "status", resp.StatusCode, "error", u.line(head), "attempt", attempt, "retrying", attempt == 1 && u.replayable)
	return resp, true, nil
}

// line is the first line of an upstream error, or the line holding the commit_refs error inside a push report, with the
// token and any credentials in a URL removed, cut to lineLimit bytes.
func (u *retrier) line(body []byte) string {
	text := string(body)
	if i := strings.Index(text, commitRefsError); i >= 0 {
		text = text[strings.LastIndexFunc(text[:i], func(r rune) bool { return r < ' ' })+1:]
	}
	text = strings.TrimLeft(text, " \t\r\n")
	if i := strings.IndexFunc(text, func(r rune) bool { return r < ' ' || r == 0x7f }); i >= 0 {
		text = text[:i]
	}
	if u.token != "" {
		text = strings.ReplaceAll(text, u.token, "[redacted]")
	}
	text = urlCredentials.ReplaceAllString(text, "://[redacted]@")
	if len(text) > lineLimit {
		text = strings.ToValidUTF8(text[:lineLimit], "") + "…"
	}
	return strings.TrimSpace(text)
}

// frameReport writes a receive-pack report, in side-band-64k packets when the client asked for them.
func frameReport(w io.Writer, report *bytes.Buffer, sideband bool) {
	if !sideband {
		_, _ = report.WriteTo(w)
		return
	}
	for report.Len() > 0 {
		pkt(w, "\x01"+string(report.Next(min(report.Len(), 65515))))
	}
	io.WriteString(w, "0000")
}

func validPart(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func pkt(w io.Writer, value string) {
	fmt.Fprintf(w, "%04x%s", len(value)+4, value)
}

func commands(body io.ReadCloser) (io.ReadCloser, []string, bool, error) {
	reader := bufio.NewReader(body)
	var prefix bytes.Buffer
	var refs []string
	var sideband bool
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(reader, hdr[:]); err != nil {
			return nil, nil, false, err
		}
		prefix.Write(hdr[:])
		n, err := strconv.ParseUint(string(hdr[:]), 16, 16)
		if err != nil || n > 65520 || (n != 0 && n < 4) || prefix.Len()+int(n) > 1<<20 {
			return nil, nil, false, fmt.Errorf("bad packet")
		}
		if n == 0 {
			break
		}
		line := make([]byte, int(n)-4)
		if _, err := io.ReadFull(reader, line); err != nil {
			return nil, nil, false, err
		}
		prefix.Write(line)
		// A push from a shallow clone names its shallow commits first, one "shallow <oid>" line each, before the commands.
		if oid, ok := strings.CutPrefix(strings.TrimSuffix(string(line), "\n"), "shallow "); ok && len(refs) == 0 && validOID(oid) {
			continue
		}
		command, caps, hasCaps := strings.Cut(string(line), "\x00")
		if hasCaps {
			for _, cap := range strings.Fields(caps) {
				if cap == "side-band-64k" {
					sideband = true
				}
			}
		}
		fields := strings.Fields(command)
		if len(fields) != 3 {
			return nil, nil, false, fmt.Errorf("bad command")
		}
		refs = append(refs, fields[2])
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(&prefix, reader), body}, refs, sideband, nil
}

// checkPush checks all commands before reserving any names or forwarding the pack. A secondary push of a
// session's own name reserves it transactionally; a PR or resumed run grants access without transferring ownership.
func checkPush(ctx context.Context, rooms RoomRepositories, defaultBranch func(context.Context, contract.RepoName, bool) (string, error), source, repo contract.RepoName, session contract.SessionID, refs []string) (bool, error) {
	if len(refs) == 0 {
		return true, nil
	}
	var claims []string
	for _, ref := range refs {
		branch, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok {
			return false, nil
		}
		allowed, own, err := pushAccess(ctx, rooms, source, repo, session, branch)
		if !allowed || err != nil {
			return false, err
		}
		if own && source != repo {
			claims = append(claims, branch)
		}
	}
	base, err := defaultBranch(ctx, repo, repo != source)
	if err != nil {
		return false, err
	}
	if base != "" && slices.Contains(refs, "refs/heads/"+base) {
		return false, nil
	}
	if len(claims) != 0 {
		if err := rooms.RecordPushedBranches(ctx, source, repo, session, claims, time.Now()); err != nil {
			if errors.Is(err, store.ErrBranchTaken) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// pushRule is what a refused push reads: the refusal and the rule that made it, naming the session's branch.
func pushRule(ctx context.Context, rooms RoomRepositories, source, repo contract.RepoName, session contract.SessionID) (string, error) {
	recorded, err := rooms.SessionBranches(ctx, source, session)
	if err != nil {
		return "", err
	}
	if len(recorded) == 0 {
		return "not owned by this session: it has no branch yet, and can push the branch of an open PR it owns, or an open PR owned by a session in its parent's team (its own team for root) or above it", nil
	}
	rule := fmt.Sprintf("not owned by this session: it can push %s, %s-<topic>, those branches of a workflow run it resumes, and the branch of an open PR it owns, or an open PR owned by a session in its parent's team (its own team for root) or above it", recorded[0], recorded[0])
	if repo != source {
		rule += fmt.Sprintf("; to push its own names on %s, they must not be held by another open session there or on %s", repo, source)
	}
	return rule + "; main, master, HEAD and the default branch are protected", nil
}

// refSafe is a name or topic of letters, digits, dots, underscores and hyphens that git accepts in a branch name.
func refSafe(s string) bool {
	return validPart(s) && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, ".lock") && !strings.Contains(s, "..")
}

// ValidBranch reports whether name is one the engine records as a branch: refSafe and starting with a letter or digit.
func ValidBranch(name string) bool {
	return refSafe(name) && name[0] != '.' && name[0] != '-' && name[0] != '_'
}

// CanPushBranch checks branch ownership on a room repository without recording a claim or consulting GitHub.
func CanPushBranch(ctx context.Context, rooms RoomRepositories, repo contract.RepoName, session contract.SessionID, branch string) (bool, error) {
	selected, err := rooms.Session(ctx, session)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if repo != selected.Repo {
		repos, err := rooms.RoomRepos(ctx, selected.Room)
		if err != nil {
			return false, err
		}
		if !slices.ContainsFunc(repos, func(item contract.RoomRepo) bool { return item.Repo == repo }) {
			return false, nil
		}
	}
	allowed, _, err := pushAccess(ctx, rooms, selected.Repo, repo, session, branch)
	return allowed, err
}

// CanRunActionsOnBranch keeps the existing Actions rights: the session's own and resumed branches on its
// selected repository, plus PR branch rights. A secondary push or reservation does not grant Actions writes.
func CanRunActionsOnBranch(ctx context.Context, rooms RoomRepositories, repo contract.RepoName, session contract.SessionID, branch string) (bool, error) {
	selected, err := rooms.Session(ctx, session)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if repo != selected.Repo {
		return rooms.OwnsPRBranch(ctx, repo, session, branch)
	}
	allowed, _, err := pushAccess(ctx, rooms, selected.Repo, repo, session, branch)
	return allowed, err
}

// pushAccess reports whether the name is reachable, and whether that right comes from the session's own branch.
// Only its selected repository supplies names: recording work-topic on a second repository must not make
// work-topic a new base there. PR grants and branch holders are checked on the destination repository.
func pushAccess(ctx context.Context, rooms RoomRepositories, source, repo contract.RepoName, session contract.SessionID, branch string) (allowed, own bool, err error) {
	if branch == "main" || branch == "master" || branch == "HEAD" {
		return false, false, nil
	}
	bases, err := rooms.SessionBranches(ctx, source, session)
	if err != nil {
		return false, false, err
	}
	if match, err := reaches(ctx, rooms, source, repo, []contract.SessionID{session}, bases, branch); match || err != nil {
		return match, match, err
	}
	if owned, err := rooms.OwnsPRBranch(ctx, repo, session, branch); owned || err != nil {
		return owned, false, err
	}
	resumed, err := rooms.ResumedSessions(ctx, source, session)
	if err != nil {
		return false, false, err
	}
	holders := append([]contract.SessionID{session}, resumed...)
	for _, earlier := range resumed {
		recorded, err := rooms.SessionBranches(ctx, source, earlier)
		if err != nil {
			return false, false, err
		}
		if match, err := reaches(ctx, rooms, source, repo, holders, recorded, branch); match || err != nil {
			return match, false, err
		}
	}
	return false, false, nil
}

// reaches reports whether branch is one of recorded or a topic of one, and no session outside sessions has it
// in the repository that supplied the name or the repository receiving the push.
func reaches(ctx context.Context, rooms RoomRepositories, source, repo contract.RepoName, sessions []contract.SessionID, recorded []string, branch string) (bool, error) {
	for _, base := range recorded {
		topic, ok := strings.CutPrefix(branch, base+"-")
		if branch != base && !(ok && refSafe(topic)) {
			continue
		}
		holder, err := rooms.BranchHolder(ctx, source, branch)
		if err != nil || holder != "" && !slices.Contains(sessions, holder) {
			return false, err
		}
		if source != repo {
			holder, err = rooms.BranchHolder(ctx, repo, branch)
			return holder == "" || slices.Contains(sessions, holder), err
		}
		return true, nil
	}
	return false, nil
}

// validOID is a full hexadecimal object name, SHA-1 or SHA-256.
func validOID(oid string) bool {
	if len(oid) != 40 && len(oid) != 64 {
		return false
	}
	_, err := hex.DecodeString(oid)
	return err == nil
}
