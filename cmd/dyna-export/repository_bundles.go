package main

import (
	"database/sql"
	"fmt"
	"sort"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

type repoGap struct {
	Identity   string              `json:"identity"`
	Kind       string              `json:"kind"`
	Rooms      []contract.RoomName `json:"rooms,omitempty"`
	Placements []string            `json:"placements,omitempty"`
	Reason     string              `json:"reason"`
}

type repoOutput struct {
	Repos        []contract.RepoBundle
	RoomRepos    map[contract.RoomName][]contract.RoomRepo
	RoomHandoffs map[contract.RoomName][]contract.RepoHandoff
	RepoRooms    map[contract.RepoName][]contract.RoomName
	Gaps         []repoGap
}

func buildRepositories(db *sql.DB, records []room.Record, cap capture) (repoOutput, error) {
	out := repoOutput{RoomRepos: make(map[contract.RoomName][]contract.RoomRepo), RoomHandoffs: make(map[contract.RoomName][]contract.RepoHandoff), RepoRooms: make(map[contract.RepoName][]contract.RoomName)}
	members := make(map[contract.RepoName][]room.Record)
	for _, rec := range records {
		var paths []string
		if rec.Repo != "" {
			paths = append(paths, rec.Repo)
		}
		paths = append(paths, rec.ExtraRepos...)
		seen := make(map[contract.RepoName]bool)
		for _, p := range paths {
			id, ok := cap.Repos[p]
			if !ok {
				return out, fmt.Errorf("repository path missing from capture manifest")
			}
			if seen[id] {
				return out, fmt.Errorf("duplicate room repository")
			}
			seen[id] = true
			members[id] = append(members[id], rec)
			name := contract.RoomName(rec.Name)
			out.RepoRooms[id] = append(out.RepoRooms[id], name)
			repo, supported := githubRepo(string(id))
			if !supported {
				out.RoomHandoffs[name] = append(out.RoomHandoffs[name], contract.RepoHandoff{Identity: string(id), Reason: contract.RepoUnsupportedForge})
				continue
			}
			out.RoomRepos[name] = append(out.RoomRepos[name], contract.RoomRepo{Repo: repo, Default: p == rec.Repo})
		}
	}
	keys := make([]string, 0, len(members))
	for id := range members {
		keys = append(keys, string(id))
	}
	sort.Strings(keys)
	for _, key := range keys {
		id := contract.RepoName(key)
		rooms := members[id]
		defaultRoom, ok := cap.Defaults[id]
		if !ok {
			return out, fmt.Errorf("repository default room missing")
		}
		found := false
		for _, rec := range rooms {
			if contract.RoomName(rec.Name) == defaultRoom {
				found = true
			}
		}
		if !found {
			return out, fmt.Errorf("repository default room does not hold repository")
		}
		repo, supported := githubRepo(key)
		if !supported {
			out.Gaps = append(out.Gaps, repoGap{Identity: key, Reason: "unsupportedForge"})
			continue
		}
		// v1 queue eligibility includes runtime and room policy; until it maps
		// cleanly the conservative manual setting is reported as a gap.
		bundle := contract.RepoBundle{Repo: repo, DefaultRoom: defaultRoom, MergePolicy: contract.MergeManual, CIMode: contract.CIGitHub}
		out.Gaps = append(out.Gaps, repoGap{Identity: key, Reason: "mergePolicy"})
		for _, rec := range rooms {
			var mode string
			err := db.QueryRow("SELECT mode FROM local_ci_modes WHERE room_id=?", rec.ID).Scan(&mode)
			if err != nil && err != sql.ErrNoRows {
				return out, err
			}
			if mode == "local" || mode == "both" {
				name := contract.RoomName(rec.Name)
				out.RoomHandoffs[name] = append(out.RoomHandoffs[name], contract.RepoHandoff{Identity: key, Reason: contract.RepoLocalCI})
				out.Gaps = append(out.Gaps, repoGap{Identity: key, Kind: "local_ci_modes.mode", Rooms: []contract.RoomName{name}, Reason: "localCI"})
			}
		}
		for _, kind := range []room.AttributionKind{room.AttributionPR, room.AttributionComment} {
			var text string
			var placements []string
			var names []contract.RoomName
			conflict := false
			for i, rec := range rooms {
				value, placement := rec.Attribution, string(rec.AttributionPlacement)
				if override, ok := rec.AttributionKinds[kind]; ok {
					value, placement = override.Text, string(override.Placement)
				}
				if i == 0 {
					text = value
				} else if text != value {
					conflict = true
				}
				names = append(names, contract.RoomName(rec.Name))
				placements = append(placements, placement)
			}
			if conflict {
				out.Gaps = append(out.Gaps, repoGap{Identity: key, Kind: string(kind), Rooms: names, Reason: "attributionConflict"})
				continue
			}
			if text != "" {
				bundle.Attribution = append(bundle.Attribution, contract.AttributionLine{Kind: contract.AttributionKind(kind), Line: text})
				out.Gaps = append(out.Gaps, repoGap{Identity: key, Kind: string(kind), Placements: placements, Reason: "attributionPlacement"})
			}
		}
		out.Repos = append(out.Repos, bundle)
	}
	return out, nil
}
