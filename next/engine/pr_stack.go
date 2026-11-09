package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

// PrStack creates an immutable GitHub stack; it never merges or rebases it.
func (s *Service) PrStack(token string, endpoint *url.URL, transport http.RoundTripper) approve.Entry {
	client := http.Client{Transport: transport, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
		target := *endpoint
		target.Path = strings.TrimRight(target.Path, "/") + path
		req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
		return client.Do(req)
	}
	get := func(ctx context.Context, path string, out any) error {
		resp, err := request(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("GitHub HTTP %d while reading %s", resp.StatusCode, path)
		}
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
	}
	return approve.Entry{
		Scope: contract.ScopeOperate,
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassAuto },
		Card: func(_ context.Context, a contract.OpArgs, _ contract.Principal) contract.Card {
			return contract.Card{Title: "asks to create a pull request stack", Body: fmt.Sprint(a.(contract.PrStackArgs).PRs)}
		},
		Decode: func(b json.RawMessage) (contract.OpArgs, error) {
			var a contract.PrStackArgs
			err := json.Unmarshal(b, &a)
			return a, err
		},
		Check: func(ctx context.Context, a contract.OpArgs, who contract.Principal) error {
			prs := a.(contract.PrStackArgs).PRs
			invalid := func(n int, rule string) error { return fmt.Errorf("%w: PR #%d: %s", approve.ErrInvalid, n, rule) }
			first := 0
			if len(prs) > 0 {
				first = prs[0]
			}
			if who.Kind != contract.PrincipalSession {
				return invalid(first, "a session caller is required")
			}
			if len(prs) < 2 || len(prs) > 20 {
				return invalid(first, "a stack needs 2 to 20 PRs")
			}
			seen := map[int]bool{}
			for _, n := range prs {
				if n <= 0 || seen[n] {
					return invalid(n, "PR numbers must be positive and unique")
				}
				seen[n] = true
			}
			session, err := s.store.Session(ctx, who.Session)
			if err != nil || session.Repo == "" || session.Closing {
				return invalid(first, "caller needs an open session repository")
			}
			if err := s.store.CheckPRStack(ctx, session.Repo, prs, who.Session, session.Room); err != nil {
				return fmt.Errorf("%w: %v", approve.ErrInvalid, err)
			}
			if token == "" {
				return invalid(first, "GitHub token is not set")
			}
			path := "/repos/" + string(session.Repo)
			var repo struct {
				DefaultBranch string `json:"default_branch"`
			}
			if err := get(ctx, path, &repo); err != nil {
				return invalid(first, err.Error())
			}
			if repo.DefaultBranch == "" {
				return invalid(first, "repository default branch is unavailable")
			}
			base := repo.DefaultBranch
			for _, n := range prs {
				var pr struct {
					Number int             `json:"number"`
					State  string          `json:"state"`
					Stack  json.RawMessage `json:"stack"`
					Head   struct {
						Ref  string `json:"ref"`
						Repo struct {
							FullName string `json:"full_name"`
						} `json:"repo"`
					} `json:"head"`
					Base struct {
						Ref  string `json:"ref"`
						Repo struct {
							FullName string `json:"full_name"`
						} `json:"repo"`
					} `json:"base"`
				}
				if err := get(ctx, fmt.Sprintf("%s/pulls/%d", path, n), &pr); err != nil {
					return invalid(n, err.Error())
				}
				switch {
				case pr.Number != n || pr.State != "open":
					return invalid(n, "must be an open PR with the requested number")
				case !strings.EqualFold(pr.Base.Repo.FullName, string(session.Repo)):
					return invalid(n, "base repository must be the session repository")
				case !strings.EqualFold(pr.Head.Repo.FullName, string(session.Repo)):
					return invalid(n, "fork PRs cannot be stacked")
				case len(pr.Stack) > 0 && string(pr.Stack) != "null":
					return invalid(n, "already belongs to a stack")
				case pr.Base.Ref != base:
					return invalid(n, "base must be "+base)
				case pr.Head.Ref == "":
					return invalid(n, "head branch is missing")
				}
				base = pr.Head.Ref
			}
			return nil
		},
		Execute: func(ctx context.Context, _ contract.OpID, a contract.OpArgs, who contract.Principal) contract.OpResult {
			session, err := s.store.Session(ctx, who.Session)
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: "session repository unavailable"}
			}
			prs := a.(contract.PrStackArgs).PRs
			body, _ := json.Marshal(struct {
				PRs []int `json:"pull_requests"`
			}{prs})
			resp, err := request(ctx, http.MethodPost, "/repos/"+string(session.Repo)+"/stacks", bytes.NewReader(body))
			if err != nil {
				return contract.OpResult{Cause: contract.CauseGithubFailed, Error: "GitHub request failed; stack outcome unknown, reconcile before retrying"}
			}
			defer resp.Body.Close()
			var payload struct {
				Number  int    `json:"number"`
				Message string `json:"message"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&payload)
			if resp.StatusCode != http.StatusCreated {
				return contract.OpResult{Cause: contract.CauseGithubFailed, Error: strings.ReplaceAll(fmt.Sprintf("GitHub HTTP %d: %s", resp.StatusCode, payload.Message), token, "***")}
			}
			if err != nil || payload.Number <= 0 {
				return contract.OpResult{Cause: contract.CauseGithubFailed, Error: "GitHub created a stack but returned an invalid response; reconcile before retrying"}
			}
			link := fmt.Sprintf("https://github.com/%s/stack/%d", session.Repo, payload.Number)
			result := contract.OpResult{Stack: &contract.PrStackResult{Stack: payload.Number, URL: link}, Output: fmt.Sprintf("stack #%d created: %s", payload.Number, link)}
			if err := s.store.RecordPRStack(ctx, session.Repo, prs, who.Session, session.Room, payload.Number); err != nil {
				result.Output += fmt.Sprintf("\nStack was created, but recording all layer positions failed (%v); ownership was not changed. Reconcile tracking before retrying.", err)
			}
			return result
		},
	}
}
