package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// githubWritesRefusal is a call that the githubWrites setting refused. The cause says why, and the message says what the setting is.
func githubWritesRefusal(code contract.ErrorCode, reason contract.GitHubWriteReason, message string) *contract.Error {
	return &contract.Error{Code: code, Message: message + " (room setting githubWrites)", GitHubWrites: &contract.GitHubWritesRefused{Reason: reason}}
}

// checkedGitHubWrites validates a githubWrites value once, at the boundary that owns it: every group is known and every repository
// is one the room lists, or `*`. It returns the value to store: groups in the order of AllGitHubWriteGroups, repositories spelled as
// the room lists them, and `*` alone when it was given. No groups is off and stores nothing else.
func (s *Service) checkedGitHubWrites(ctx context.Context, room contract.RoomID, asked contract.GitHubWrites) (contract.GitHubWrites, *contract.Error) {
	var checked contract.GitHubWrites
	for _, group := range asked.Groups {
		if !slices.Contains(contract.AllGitHubWriteGroups, group) {
			return checked, githubWritesRefusal(contract.ErrInvalid, contract.GitHubWriteUnknownGroup, fmt.Sprintf("githubWrites has no group %q; the groups are %v", group, contract.AllGitHubWriteGroups))
		}
	}
	listed, err := s.store.RoomRepos(ctx, room)
	if errors.Is(err, store.ErrRoomNotFound) {
		return checked, &contract.Error{Code: contract.ErrNotFound, Message: "no room has that ID"}
	}
	if err != nil {
		return checked, failure(err)
	}
	if len(asked.Groups) == 0 {
		return checked, nil
	}
	if len(asked.Repos) == 0 {
		return checked, invalid("githubWrites names groups and no repository; give the room's repositories, or *")
	}
	for _, repo := range asked.Repos {
		if repo == contract.GitHubWriteAllRepos {
			checked.Repos = []contract.RepoName{contract.GitHubWriteAllRepos}
			break
		}
		at := slices.IndexFunc(listed, func(each contract.RoomRepo) bool { return strings.EqualFold(string(each.Repo), string(repo)) })
		if at < 0 {
			return checked, githubWritesRefusal(contract.ErrInvalid, contract.GitHubWriteRepoNotInRoom, fmt.Sprintf("repository %q is not one of this room's repositories, which are %v; githubWrites takes those, or *", repo, listed))
		}
		if !slices.Contains(checked.Repos, listed[at].Repo) {
			checked.Repos = append(checked.Repos, listed[at].Repo)
		}
	}
	for _, group := range contract.AllGitHubWriteGroups {
		if slices.Contains(asked.Groups, group) {
			checked.Groups = append(checked.Groups, group)
		}
	}
	return checked, nil
}

// GitHubWritesOf is the writes a room's boxes may make now: the room's setting with `*` replaced by the room's repositories, and
// only repositories the room still lists. A room with the setting off, or one that is gone, has none.
func (s *Service) GitHubWritesOf(ctx context.Context, room contract.RoomID) (contract.GitHubWrites, error) {
	stored, err := s.store.RoomGitHubWrites(ctx, room)
	if err != nil || !stored.On() {
		return contract.GitHubWrites{}, err
	}
	listed, err := s.store.RoomRepos(ctx, room)
	if err != nil {
		return contract.GitHubWrites{}, err
	}
	granted := stored.Expanded(listed)
	granted.Repos = slices.DeleteFunc(slices.Clone(granted.Repos), func(repo contract.RepoName) bool {
		return !slices.ContainsFunc(listed, func(each contract.RoomRepo) bool { return each.Repo == repo })
	})
	return granted, nil
}
