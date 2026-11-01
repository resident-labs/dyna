package ci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/actions/scaleset"
	"github.com/actions/scaleset/listener"
	"github.com/resident-labs/dyna/contract"
)

// defaultRunnerGroup is the repository-level group every repository has. The
// organization's own groups stay out of it, so no organization setting changes.
const defaultRunnerGroup = 1

// Sets adapts the actions/scaleset client, the one file that imports it. It
// registers at repository level: an organization-level set would need the
// organization's runner group to allow public repositories.
type Sets struct {
	Token string
	// Name is the scale set's name.
	Name string

	mu   sync.Mutex
	sets map[contract.RepoName]*set
}

type set struct {
	client *scaleset.Client
	id     int
}

func (s *Sets) open(ctx context.Context, repo contract.RepoName) (*set, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if found := s.sets[repo]; found != nil {
		return found, nil
	}
	client, err := scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
		GitHubConfigURL:     "https://github.com/" + string(repo),
		PersonalAccessToken: s.Token,
		SystemInfo:          scaleset.SystemInfo{System: "dyna"},
	})
	if err != nil {
		return nil, err
	}
	found, err := client.GetRunnerScaleSet(ctx, defaultRunnerGroup, s.Name)
	if err != nil {
		return nil, err
	}
	if found == nil {
		// Updates stay on: GitHub stops serving a runner about a month after a newer
		// release, and a runner too old to update would stop every job.
		found, err = client.CreateRunnerScaleSet(ctx, &scaleset.RunnerScaleSet{Name: s.Name, RunnerGroupID: defaultRunnerGroup, Labels: []scaleset.Label{{Name: s.Name}}})
		if err != nil {
			return nil, err
		}
	}
	opened := &set{client: client, id: found.ID}
	if s.sets == nil {
		s.sets = make(map[contract.RepoName]*set)
	}
	s.sets[repo] = opened
	return opened, nil
}

func (s *Sets) forget(repo contract.RepoName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sets, repo)
}

type scaler struct {
	listener *listener.Listener
	scale    Scale
}

func (scaler) HandleJobStarted(context.Context, *scaleset.JobStarted) error     { return nil }
func (scaler) HandleJobCompleted(context.Context, *scaleset.JobCompleted) error { return nil }

// HandleDesiredRunnerCount keeps the capacity GitHub sees current, so a host
// that left or fell below its disk floor stops drawing jobs.
func (s scaler) HandleDesiredRunnerCount(ctx context.Context, count int) (int, error) {
	capacity := s.scale(ctx, count)
	s.listener.SetMaxRunners(capacity)
	return capacity, nil
}

// Listen holds the scale set's one message session. A second engine gets a
// conflict here and retries, so two engines cannot serve one set.
func (s *Sets) Listen(ctx context.Context, repo contract.RepoName, scale Scale) error {
	opened, err := s.open(ctx, repo)
	if err != nil {
		return err
	}
	owner, err := os.Hostname()
	if err != nil {
		return err
	}
	session, err := opened.client.MessageSessionClient(ctx, opened.id, owner)
	if err != nil {
		// The set may have been deleted by hand, so the next attempt looks it up again.
		s.forget(repo)
		return err
	}
	defer session.Close(context.WithoutCancel(ctx))
	poll, err := listener.New(session, listener.Config{ScaleSetID: opened.id})
	if err != nil {
		return err
	}
	if err := poll.Run(ctx, scaler{poll, scale}); !errors.Is(err, context.Canceled) {
		s.forget(repo)
		return err
	}
	return nil
}

// JIT generates a single-job config for one more runner in repo's scale set.
func (s *Sets) JIT(ctx context.Context, repo contract.RepoName, name string) (int64, string, error) {
	opened, err := s.open(ctx, repo)
	if err != nil {
		return 0, "", err
	}
	config, err := opened.client.GenerateJitRunnerConfig(ctx, &scaleset.RunnerScaleSetJitRunnerSetting{Name: name, WorkFolder: "/home/runner/work"}, opened.id)
	if err != nil {
		return 0, "", err
	}
	if config == nil || config.Runner == nil || config.EncodedJITConfig == "" {
		return 0, "", fmt.Errorf("GitHub returned an invalid JIT config for %s", name)
	}
	return int64(config.Runner.ID), config.EncodedJITConfig, nil
}

// Delete removes repo's scale set. A repository that never had one is done.
func (s *Sets) Delete(ctx context.Context, repo contract.RepoName) error {
	opened, err := s.open(ctx, repo)
	if err != nil {
		return err
	}
	s.forget(repo)
	return opened.client.DeleteRunnerScaleSet(ctx, opened.id)
}
