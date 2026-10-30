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
	enginegit "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/store"
)

// PrOpen registers the only PR write needed for the self-hosting trial.
func (s *Service) PrOpen(token string, endpoint *url.URL, transport http.RoundTripper) approve.Entry {
	lookup := func(ctx context.Context, who contract.Principal) (store.Session, string, error) {
		if who.Kind != contract.PrincipalSession {
			return store.Session{}, "", approve.ErrForbidden
		}
		session, err := s.store.Session(ctx, who.Session)
		return session, enginegit.SessionBranchPrefix + string(who.Session), err
	}
	return approve.Entry{
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassAuto },
		Card: func(args contract.OpArgs, _ contract.Principal) contract.Card {
			return contract.Card{Title: args.(contract.PrOpenArgs).Title}
		},
		Check: func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
			input := args.(contract.PrOpenArgs)
			session, head, err := lookup(ctx, who)
			if err != nil {
				return err
			}
			if session.Repo == "" || strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Base) == "" {
				return approve.ErrInvalid
			}
			if _, err := s.repos.Ref(ctx, session.Repo, "refs/heads/"+head); err != nil {
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
			unlock := s.lockBox(who.Session)
			defer unlock()
			session, head, err := lookup(ctx, who)
			if err != nil {
				return contract.OpResult{Error: "session repository unavailable"}
			}
			if token == "" {
				return contract.OpResult{Error: "GitHub token is not set"}
			}
			input := args.(contract.PrOpenArgs)
			// The request has only strings and a boolean, which JSON always encodes.
			body, _ := json.Marshal(struct {
				contract.PrOpenArgs
				Head string `json:"head"`
			}{input, head})
			target := *endpoint
			target.Path = strings.TrimRight(target.Path, "/") + "/repos/" + string(session.Repo) + "/pulls"
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
			if err != nil {
				return contract.OpResult{Error: "cannot create PR request"}
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			client := http.Client{Transport: transport, Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			response, err := client.Do(request)
			if err != nil {
				return contract.OpResult{Error: "GitHub request failed; outcome unknown, reconcile before retrying"}
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
				return contract.OpResult{Error: "GitHub may have opened the PR but returned an invalid response; reconcile before retrying"}
			}
			link := fmt.Sprintf("https://github.com/%s/pull/%d", session.Repo, payload.Number)
			result := contract.OpResult{Output: link, PR: &contract.PrOpenResult{Number: payload.Number, URL: link}}
			err = func() error {
				tree, err := s.store.RoomTree(ctx, session.Room)
				if err != nil {
					return err
				}
				event, err := s.event(session.Room, who, contract.PrOpened{Repo: session.Repo, Number: payload.Number, Session: who.Session, URL: link})
				if err != nil {
					return err
				}
				var deliveries []contract.Delivery
				if who.Session != tree.Root() {
					parent, err := tree.Handoff(who.Session)
					if err != nil {
						return err
					}
					deliveries = []contract.Delivery{{To: contract.Principal{Kind: contract.PrincipalSession, Session: parent.PRHolder}, Surface: contract.SurfaceSession, Reply: contract.ReplyChannel{Kind: contract.ReplyNone}, Wake: true}}
				}
				return s.store.RecordPROpen(ctx, session.Repo, payload.Number, who.Session, event, deliveries)
			}()
			if err != nil {
				result.Error = "PR opened but ownership could not be recorded; reconcile before retrying: " + link
			}
			return result
		},
	}
}
