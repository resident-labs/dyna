package room

import (
	"errors"

	"github.com/resident-labs/dyna/contract"
)

// AddRepo returns updated room rows and the repository's default room.
// Inputs are store facts; the returned slice never aliases have.
func AddRepo(room contract.RoomID, have []contract.RoomRepo, defaultRoom contract.RoomID, repo contract.RepoName, makeDefault bool) ([]contract.RoomRepo, contract.RoomID) {
	rows := append([]contract.RoomRepo(nil), have...)
	found := false
	for i := range rows {
		if rows[i].Repo == repo {
			found = true
		}
		if makeDefault {
			rows[i].Default = rows[i].Repo == repo
		}
	}
	if !found {
		rows = append(rows, contract.RoomRepo{Repo: repo, Default: makeDefault || len(have) == 0})
	}
	if defaultRoom == "" {
		defaultRoom = room
	}
	return rows, defaultRoom
}

// RepoForSession selects the explicitly requested repository, the parent's
// repository, or the room default, in that order.
func RepoForSession(rows []contract.RoomRepo, requested, parent contract.RepoName) (contract.RepoName, error) {
	selected := requested
	if selected == "" {
		selected = parent
	}
	for _, row := range rows {
		if selected != "" && row.Repo == selected || selected == "" && row.Default {
			return row.Repo, nil
		}
	}
	if selected == "" {
		return "", nil
	}
	return "", errors.New("repository is not listed in the room")
}
