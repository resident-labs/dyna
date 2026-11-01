// Package git handles the repository mirror and the box's smart-HTTP route.
package git

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
)

// Handler binds a smart-HTTP route to a session. The upstream and token belong
// to the engine, not to the box.
func Handler(upstream *url.URL, transport http.RoundTripper, token string, session contract.SessionID, room contract.RoomID, rooms RoomRepositories) http.Handler {
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.URL.Path = strings.TrimPrefix(r.In.URL.Path, "/git")
			r.Out.Host = upstream.Host
			r.Out.Header.Del("Authorization")
			r.Out.SetBasicAuth("x-access-token", token)
		},
	}
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
		if (r.Method == http.MethodPost && parts[2] == "git-receive-pack") || (r.Method == http.MethodGet && r.URL.Query().Get("service") == "git-receive-pack") {
			selected, err := rooms.Session(r.Context(), session)
			if err != nil {
				http.Error(w, "repository access unavailable", http.StatusServiceUnavailable)
				return
			}
			if repo != selected.Repo {
				if r.Method == http.MethodPost {
					_, _ = io.Copy(io.Discard, r.Body)
				}
				http.Error(w, "repository not owned by this session", http.StatusForbidden)
				return
			}
		}
		if r.Method == http.MethodPost && parts[2] == "git-receive-pack" {
			if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
				http.Error(w, "unsupported content encoding", http.StatusUnsupportedMediaType)
				return
			}
			body, rejected, sideband, err := commands(r.Context(), r.Body, session, repo, rooms)
			if err != nil {
				http.Error(w, "invalid receive-pack commands", http.StatusBadRequest)
				return
			}
			if len(rejected) != 0 {
				if _, err := io.Copy(io.Discard, body); err != nil {
					http.Error(w, "invalid receive-pack body", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
				var report bytes.Buffer
				pkt(&report, "unpack ok\n")
				for _, ref := range rejected {
					pkt(&report, "ng "+ref+" not owned by this session\n")
				}
				io.WriteString(&report, "0000")
				if sideband {
					for report.Len() > 0 {
						size := min(report.Len(), 65515)
						pkt(w, "\x01"+string(report.Next(size)))
					}
					io.WriteString(w, "0000")
				} else {
					_, _ = report.WriteTo(w)
				}
				return
			}
			r.Body = body
		}
		proxy.ServeHTTP(w, r)
	})
}

// NotifyPush calls pushed after a push reaches next, so the PR poll looks sooner (spec 7.7). A push the route
// refused also counts: it costs at most an earlier poll, within the same limit.
func NotifyPush(next http.Handler, pushed func(contract.RepoName)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/git/"), "/")
		if r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "git-receive-pack" {
			pushed(contract.RepoName(parts[0] + "/" + strings.TrimSuffix(parts[1], ".git")))
		}
	})
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

func commands(ctx context.Context, body io.ReadCloser, session contract.SessionID, repo contract.RepoName, rooms RoomRepositories) (io.ReadCloser, []string, bool, error) {
	reader := bufio.NewReader(body)
	var prefix bytes.Buffer
	var refs []string
	var denied, sideband bool
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
		ref := fields[2]
		allowed := ref == "refs/dyna/archive/"+string(session)
		if branch, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			var err error
			allowed, err = CanPushBranch(ctx, rooms, repo, session, branch)
			if err != nil {
				return nil, nil, false, err
			}
		}
		refs = append(refs, ref)
		if !allowed {
			denied = true
		}
	}
	if !denied {
		refs = nil
	}
	return struct {
		io.Reader
		io.Closer
	}{io.MultiReader(&prefix, reader), body}, refs, sideband, nil
}

// CanPushBranch checks a session branch or a branch registered to an owned PR.
func CanPushBranch(ctx context.Context, rooms RoomRepositories, repo contract.RepoName, session contract.SessionID, branch string) (bool, error) {
	base := SessionBranchPrefix + string(session)
	if branch == base {
		return true, nil
	}
	if topic, ok := strings.CutPrefix(branch, base+"-"); ok && validPart(topic) && !strings.HasSuffix(topic, ".") && !strings.HasSuffix(topic, ".lock") && !strings.Contains(topic, "..") {
		return true, nil
	}
	return rooms.OwnsPRBranch(ctx, repo, session, branch)
}
