package view

import (
	"context"
	"dyna-agent/internal/ghclient"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"dyna-agent/internal/pr/watch"
)

// prNoiseRepos includes the room's GitHub remote and repositories of PRs
// tracked in this room, including ended PRs. Host is part of every scope.
func (s *Service) prNoiseRepos(roomRef string) ([]watch.Ref, string, error) {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return nil, "", err
	}
	st, err := s.Watcher().Store.Read()
	if err != nil {
		return nil, "", err
	}
	byRepo := map[string]watch.Ref{}
	add := func(ref watch.Ref) {
		if ref.Host == "" || ref.Repo == "" {
			return
		}
		ref.Host, ref.Repo = strings.ToLower(ref.Host), strings.ToLower(ref.Repo)
		byRepo[ref.Host+"/"+ref.Repo] = ref
	}
	for _, r := range st.Records {
		if r.RoomID == rec.ID {
			add(watch.Ref{Host: r.Host, Repo: r.Repo})
		}
	}
	warning := ""
	if rec.Repo != "" {
		ref, lookupErr := s.roomGitHubRepo(rec.ID)
		if lookupErr != nil {
			if len(byRepo) == 0 {
				return nil, "", lookupErr
			}
			warning = "Could not read the room repository. Showing rules for its tracked PR repositories only. " + lookupErr.Error()
		} else {
			add(ref)
		}
	}
	out := make([]watch.Ref, 0, len(byRepo))
	for _, ref := range byRepo {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host+"/"+out[i].Repo < out[j].Host+"/"+out[j].Repo })
	return out, warning, nil
}

func (s *Service) roomGitHubRepo(roomRef string) (watch.Ref, error) {
	root, err := s.WorkspaceRoot(roomRef, "")
	if err != nil {
		return watch.Ref{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
	defer cancel()
	ctx = ghclient.WithRoom(ghclient.WithRooms(ctx, s.Notify.Rooms), roomRef)
	b, err := runGH(ctx, root, "repo", "view", "--json", "nameWithOwner,url")
	if err != nil {
		return watch.Ref{}, err
	}
	var repo struct {
		Name string `json:"nameWithOwner"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(b, &repo); err != nil {
		return watch.Ref{}, fmt.Errorf("read GitHub repository: %w", err)
	}
	u, err := url.Parse(repo.URL)
	parts := strings.Split(repo.Name, "/")
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return watch.Ref{}, fmt.Errorf("gh returned an invalid repository")
	}
	return watch.Ref{Host: u.Host, Repo: repo.Name}, nil
}

func (s *Service) PRNoiseRules(roomRef string) (PRNoiseList, error) {
	repos, warning, err := s.prNoiseRepos(roomRef)
	if err != nil {
		return PRNoiseList{}, err
	}
	rules, err := s.Watcher().ListNoise()
	if err != nil {
		return PRNoiseList{}, err
	}
	out := PRNoiseList{Repositories: []string{}, Rules: []watch.NoiseRule{}, Warning: warning}
	for _, repo := range repos {
		out.Repositories = append(out.Repositories, repo.Host+"/"+repo.Repo)
	}
	for _, rule := range rules {
		for _, repo := range repos {
			if strings.EqualFold(rule.Host, repo.Host) && strings.EqualFold(rule.Repo, repo.Repo) {
				out.Rules = append(out.Rules, rule)
				break
			}
		}
	}
	return out, nil
}

func (s *Service) RemovePRNoiseRule(ctx context.Context, roomRef, id string) error {
	rec, err := s.Notify.Rooms.Resolve(roomRef)
	if err != nil {
		return err
	}
	if err := checkIn(ctx, "git pr noise rm", rec.ID); err != nil {
		return err
	}
	repos, _, err := s.prNoiseRepos(rec.ID)
	if err != nil {
		return err
	}
	return s.Watcher().RemoveNoiseInRepos(id, repos)
}
