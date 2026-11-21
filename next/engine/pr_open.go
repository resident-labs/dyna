package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/store"
)

// PrOpen registers pull request creation.
func (s *Service) PrOpen(token string, endpoint *url.URL, transport http.RoundTripper) approve.Entry {
	return s.accountEntry(token, func(credential string) approve.Entry { return s.prOpen(credential, endpoint, transport) })
}
func (s *Service) prOpen(token string, endpoint *url.URL, transport http.RoundTripper) approve.Entry {
	lookup := func(ctx context.Context, who contract.Principal, input contract.PrOpenArgs) (store.Session, contract.RepoName, string, error) {
		if who.Kind != contract.PrincipalSession {
			return store.Session{}, "", "", approve.ErrForbidden
		}
		session, err := s.store.Session(ctx, who.Session)
		if err != nil {
			return session, "", "", err
		}
		repo := input.Repo
		if repo == "" {
			repo = session.Repo
		}
		if repo == "" {
			return session, "", "", fmt.Errorf("%w: the session has no repository; specify repo", approve.ErrInvalid)
		}
		repos, err := s.store.RoomRepos(ctx, session.Room)
		if err != nil {
			return session, "", "", err
		}
		if !slices.ContainsFunc(repos, func(item contract.RoomRepo) bool { return item.Repo == repo }) {
			return session, "", "", fmt.Errorf("%w: repository %s is not in the room", approve.ErrForbidden, repo)
		}
		head := input.Head
		if head == "" {
			recorded, err := s.store.SessionBranches(ctx, session.Repo, who.Session)
			if err != nil {
				return session, "", "", err
			}
			if len(recorded) == 0 {
				return session, "", "", fmt.Errorf("%w: the session has no branch recorded yet; head was not given", approve.ErrInvalid)
			}
			head = recorded[0]
		}
		return session, repo, head, nil
	}
	return approve.Entry{
		Scope: contract.ScopeOperate,
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassAuto },
		Card: func(_ context.Context, args contract.OpArgs, _ contract.Principal) contract.Card {
			return contract.Card{Title: "asks to open a pull request", Body: args.(contract.PrOpenArgs).Title}
		},
		Check: func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
			input := args.(contract.PrOpenArgs)
			session, repo, head, err := lookup(ctx, who, input)
			if err != nil {
				return err
			}
			allowed, err := enginegit.CanPushBranch(ctx, s.store, repo, who.Session, head)
			if err != nil {
				return err
			}
			if !allowed {
				own, err := s.store.SessionBranches(ctx, session.Repo, who.Session)
				if err != nil {
					return err
				}
				if len(own) == 0 {
					return fmt.Errorf("%w: branch %s is not recorded for this session or a session under it, and this session has no branch yet; a session's branch is recorded when its box is set up with the repository, so open the PR from a session that has one", approve.ErrForbidden, head)
				}
				return fmt.Errorf("%w: branch %s is not owned by this session on %s; push to %s, or to a name that starts with %s-, and open the PR from that", approve.ErrForbidden, head, repo, strings.Join(own, " or "), own[0])
			}
			switch {
			case strings.TrimSpace(input.Title) == "":
				return fmt.Errorf("%w: title is required", approve.ErrInvalid)
			case strings.TrimSpace(input.Base) == "":
				return fmt.Errorf("%w: base is required, such as main", approve.ErrInvalid)
			}
			if _, err := s.repos.Ref(ctx, repo, "refs/heads/"+head); err != nil {
				if input.Head == "" {
					return fmt.Errorf("%w: branch %s not found on %s; head was not given and defaulted to it; head names another branch", approve.ErrInvalid, head, repo)
				}
				return fmt.Errorf("%w: branch %s not found on %s; push it first", approve.ErrInvalid, head, repo)
			}
			if repo != session.Repo {
				base, err := s.repos.DefaultBranch(ctx, repo)
				if err != nil {
					return fmt.Errorf("%w: cannot read the default branch on %s", approve.ErrInvalid, repo)
				}
				if head == base {
					return fmt.Errorf("%w: branch %s is the protected default branch on %s", approve.ErrForbidden, head, repo)
				}
			}
			return nil
		},
		Decode: func(data json.RawMessage) (contract.OpArgs, error) {
			var input contract.PrOpenArgs
			err := json.Unmarshal(data, &input)
			return input, err
		},
		Execute: func(ctx context.Context, _ contract.OpID, args contract.OpArgs, who contract.Principal) contract.OpResult {
			input := args.(contract.PrOpenArgs)
			session, repo, head, err := lookup(ctx, who, input)
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
			}
			defaulted := input.Head == ""
			if token == "" {
				return contract.OpResult{Cause: contract.CauseInternal, Error: "GitHub token is not set"}
			}
			// Repo selects the URL, not a field in GitHub's request body.
			input.Repo, input.Head = "", head
			body, _ := json.Marshal(input)
			target := *endpoint
			target.Path = strings.TrimRight(target.Path, "/") + "/repos/" + string(repo) + "/pulls"
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: "cannot create PR request"}
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			client := http.Client{Transport: transport, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return contract.OpResult{Cause: contract.CauseGithubFailed, Error: "GitHub request failed; outcome unknown, reconcile before retrying"}
			}
			defer response.Body.Close()
			var payload struct {
				Number  int    `json:"number"`
				Message string `json:"message"`
				Errors  []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload)
			if response.StatusCode != http.StatusCreated {
				parts := []string{fmt.Sprintf("GitHub HTTP %d", response.StatusCode), payload.Message}
				for _, item := range payload.Errors {
					parts = append(parts, item.Message)
				}
				if defaulted && response.StatusCode == http.StatusUnprocessableEntity {
					parts = append(parts, "head was not given and defaulted to "+head+"; head names another branch")
				}
				return contract.OpResult{Cause: contract.CauseGithubFailed, Error: strings.ReplaceAll(strings.Join(parts, ": "), token, "***")}
			}
			if err != nil || payload.Number <= 0 {
				return contract.OpResult{Cause: contract.CauseGithubFailed, Error: "GitHub may have opened the PR but returned an invalid response; reconcile before retrying"}
			}
			link := fmt.Sprintf("https://github.com/%s/pull/%d", repo, payload.Number)
			result := contract.OpResult{Output: link, PR: &contract.PrOpenResult{Number: payload.Number, URL: link}}
			event, err := s.event(session.Room, who, contract.PrOpened{Repo: repo, Number: payload.Number, Session: who.Session, URL: link})
			if err == nil {
				err = s.store.RecordPROpen(ctx, repo, payload.Number, who.Session, head, input.Title, link, event, nil)
			}
			// GitHub created the PR, so a failed record is told in the output and does not fail the call.
			if err != nil {
				result.Output = fmt.Sprintf("%s\nPR #%d is open, but recording its owner failed (%v); it is not tracked, and pr track adds it", link, payload.Number, err)
			}
			return result
		},
	}
}
