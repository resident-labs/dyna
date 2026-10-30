package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	enginegit "github.com/resident-labs/dyna/engine/git"
)

// PrOpen registers the only PR write needed for the self-hosting trial.
func (s *Service) PrOpen(token string, endpoint *url.URL, transport http.RoundTripper) approve.Entry {
	lookup := func(ctx context.Context, who contract.Principal) (contract.RepoName, string, error) {
		if who.Kind != contract.PrincipalSession {
			return "", "", approve.ErrForbidden
		}
		session, err := s.store.Session(ctx, who.Session)
		if err != nil {
			return "", "", err
		}
		if session.Repo == "" {
			return "", "", fmt.Errorf("%w: session has no repository", approve.ErrInvalid)
		}
		return session.Repo, enginegit.SessionBranchPrefix + string(who.Session), nil
	}
	return approve.Entry{
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassAuto },
		Card: func(args contract.OpArgs, who contract.Principal) contract.Card {
			repo, _, _ := lookup(context.Background(), who)
			return contract.Card{Title: args.(contract.PrOpenArgs).Title, Target: contract.Target{Kind: contract.TargetRepo, Name: string(repo)}}
		},
		Check: func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
			input := args.(contract.PrOpenArgs)
			if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Base) == "" {
				return approve.ErrInvalid
			}
			repo, head, err := lookup(ctx, who)
			if err != nil {
				return err
			}
			if _, err := s.repos.Ref(ctx, repo, "refs/heads/"+head); err != nil {
				return fmt.Errorf("%w: session branch not found", approve.ErrInvalid)
			}
			return nil
		},
		Decode: func(data json.RawMessage) (contract.OpArgs, error) {
			var input contract.PrOpenArgs
			err := json.Unmarshal(data, &input)
			return input, err
		},
		Execute: func(ctx context.Context, args contract.OpArgs, who contract.Principal) contract.OpResult {
			repo, head, err := lookup(ctx, who)
			if err != nil {
				return contract.OpResult{Error: "session repository unavailable"}
			}
			if token == "" {
				return contract.OpResult{Error: "GitHub token is not set"}
			}
			input := args.(contract.PrOpenArgs)
			body, err := json.Marshal(struct {
				Title string `json:"title"`
				Body  string `json:"body"`
				Head  string `json:"head"`
				Base  string `json:"base"`
				Draft bool   `json:"draft"`
			}{input.Title, input.Body, head, input.Base, input.Draft})
			if err != nil {
				return contract.OpResult{Error: "cannot encode PR request"}
			}
			target := *endpoint
			target.Path = strings.TrimRight(target.Path, "/") + "/repos/" + string(repo) + "/pulls"
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
			if err != nil {
				return contract.OpResult{Error: "cannot create PR request"}
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Accept", "application/vnd.github+json")
			request.Header.Set("Content-Type", "application/json")
			client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return contract.OpResult{Error: "GitHub request failed"}
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
				return contract.OpResult{Error: strings.ReplaceAll(strings.Join(parts, ": "), token, "***")}
			}
			if err != nil || payload.Number <= 0 {
				return contract.OpResult{Error: "invalid GitHub PR response"}
			}
			link := "https://github.com/" + string(repo) + "/pull/" + strconv.Itoa(payload.Number)
			result := contract.OpResult{Output: link, PR: &contract.PrOpenResult{Number: payload.Number, URL: link}}
			session, err := s.store.Session(ctx, who.Session)
			if err != nil {
				result.Error = "PR opened but ownership could not be recorded: " + link
				return result
			}
			tree, err := s.store.RoomTree(ctx, session.Room)
			if err != nil {
				result.Error = "PR opened but ownership could not be recorded: " + link
				return result
			}
			event, err := s.event(session.Room, who, contract.PrOpened{Repo: repo, Number: payload.Number, Session: who.Session, URL: link})
			if err != nil {
				result.Error = "PR opened but ownership could not be recorded: " + link
				return result
			}
			var deliveries []contract.Delivery
			if who.Session != tree.Root() {
				parent, err := tree.Handoff(who.Session)
				if err != nil {
					result.Error = "PR opened but ownership could not be recorded: " + link
					return result
				}
				deliveries = []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: parent.PRHolder}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}
			}
			if err := s.store.RecordPROpen(ctx, repo, payload.Number, who.Session, event, deliveries); err != nil {
				result.Error = "PR opened but ownership could not be recorded: " + link
			}
			return result
		},
	}
}
