package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/resident-labs/dyna/contract"
)

type limitGap struct {
	Room  string `json:"room"`
	Field string `json:"field"`
}
type exportReport struct {
	Inventory             []fieldInventory    `json:"inventory"`
	SkippedHosts          []string            `json:"skippedHosts"`
	SkippedProfiles       []string            `json:"skippedProfiles"`
	UnknownModels         []profileGap        `json:"unknownModels"`
	SkippedAccounts       []string            `json:"skippedAccounts"`
	UnknownScopes         []string            `json:"unknownScopes"`
	UnsupportedHooks      []hookGap           `json:"unsupportedHooks"`
	RepositoryGaps        []repoGap           `json:"repositoryGaps"`
	LimitGaps             []limitGap          `json:"limitGaps"`
	SkippedPRs            []skippedPR         `json:"skippedPRs"`
	SkippedServices       []string            `json:"skippedServices"`
	SkippedWorkflowLeaves int                 `json:"skippedWorkflowLeaves"`
	PartialFeeds          []contract.RoomName `json:"partialFeeds"`
	Profiles              int                 `json:"profiles"`
	Repos                 int                 `json:"repos"`
	Skills                int                 `json:"skills"`
}

func buildBundle(db *sql.DB, backup, data, feeds, config, commit string, cap capture) (contract.GlobalBundle, []contract.RoomBundle, exportReport, error) {
	var g contract.GlobalBundle
	report := exportReport{Inventory: inventory()}
	records, roomIDs, err := readRooms(db)
	if err != nil {
		return g, nil, report, err
	}
	var names []contract.RoomName
	for _, rec := range records {
		names = append(names, contract.RoomName(rec.Name))
	}
	if names == nil {
		names = []contract.RoomName{}
	}
	repos, err := buildRepositories(db, records, cap)
	if err != nil {
		return g, nil, report, err
	}
	report.RepositoryGaps = repos.Gaps
	hooks, hookGaps, err := readHooks(db, repos.RepoRooms, cap, names)
	if err != nil {
		return g, nil, report, err
	}
	report.UnsupportedHooks = hookGaps
	author := func(id string) (contract.ImportedAuthor, error) { return resolveAuthor(db, id) }
	globalRules, err := readRules(db, "*", author)
	if err != nil {
		return g, nil, report, err
	}
	settings, err := readSettings(config)
	if err != nil {
		return g, nil, report, err
	}
	profiles, disabled, unknownModels, err := readProfiles(config)
	if err != nil {
		return g, nil, report, err
	}
	report.SkippedProfiles, report.UnknownModels = disabled, unknownModels
	hosts, skippedHosts, err := readHosts(db, roomIDs)
	if err != nil {
		return g, nil, report, err
	}
	report.SkippedHosts = skippedHosts
	accounts, skippedAccounts, err := readAccounts(db, roomIDs, func(s string) (contract.RepoName, error) { return repoName(cap.Repos, s) })
	if err != nil {
		return g, nil, report, err
	}
	report.SkippedAccounts = skippedAccounts
	devices, unknownScopes, err := readDevices(db)
	if err != nil {
		return g, nil, report, err
	}
	report.UnknownScopes = unknownScopes
	skills, err := readSkills(data, cap.Skills)
	if err != nil {
		return g, nil, report, err
	}
	f, err := os.Open(backup)
	if err != nil {
		return g, nil, report, err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	f.Close()
	if err != nil {
		return g, nil, report, err
	}
	g = contract.GlobalBundle{Version: contract.BundleV1, Rooms: names, Exported: time.Now().UTC(),
		Source: contract.BundleSource{Commit: commit, Backup: filepath.Base(backup), BackupSHA256: hex.EncodeToString(hash.Sum(nil))},
		Rules:  globalRules, Settings: settings, Profiles: profiles, Hosts: hosts, Accounts: accounts, Devices: devices,
		Repos: repos.Repos, Skills: skills}
	g.Counts = globalBundleCounts(g)
	report.Profiles, report.Repos, report.Skills = len(profiles), len(repos.Repos), len(skills)
	var parts []contract.RoomBundle
	for _, rec := range records {
		name := contract.RoomName(rec.Name)
		roomAuthor := func(id string) (contract.ImportedAuthor, error) {
			if id == rec.Name {
				return contract.ImportedAuthor{Kind: contract.PrincipalOwner, Label: "owner"}, nil
			}
			return author(id)
		}
		feed, err := readFeed(feeds, rec.ID)
		if err != nil {
			return g, nil, report, err
		}
		if feed.partial {
			report.PartialFeeds = append(report.PartialFeeds, name)
		}
		rules, err := readRules(db, rec.ID, roomAuthor)
		if err != nil {
			return g, nil, report, err
		}
		goals, err := readGoals(db, rec.ID, roomAuthor)
		if err != nil {
			return g, nil, report, err
		}
		issues, err := readIssues(db, rec.ID, feed, roomAuthor)
		if err != nil {
			return g, nil, report, err
		}
		prs, skipped, err := readPRs(db, rec.ID, roomAuthor)
		if err != nil {
			return g, nil, report, err
		}
		report.SkippedPRs = append(report.SkippedPRs, skipped...)
		questions, err := readQuestions(db, rec.ID, roomAuthor)
		if err != nil {
			return g, nil, report, err
		}
		handoff, err := readHandoff(db, rec.ID, rec.Name, feed, cap.Refs, roomAuthor)
		if err != nil {
			return g, nil, report, err
		}
		limits, warnings := roomLimits(rec.Limits)
		if rec.Limits.TasksMax.Off || rec.Limits.TasksMax.Value != 0 {
			report.LimitGaps = append(report.LimitGaps, limitGap{rec.Name, "TasksMax"})
		}
		if len(warnings) > 0 {
			report.LimitGaps = append(report.LimitGaps, limitGap{rec.Name, "MemoryMax"})
		}
		var nextIssue int
		if err := db.QueryRow("SELECT COALESCE(MAX(number),0)+1 FROM room_issue_numbers WHERE room_id=?", rec.ID).Scan(&nextIssue); err != nil {
			return g, nil, report, err
		}
		b := contract.RoomBundle{Version: contract.BundleV1, Name: name, Repos: repos.RoomRepos[name], Limits: limits, Color: string(rec.Color),
			Rules: rules, Goals: goals, Issues: issues, NextIssue: nextIssue, PRs: prs, Questions: questions,
			Handoff: handoff, HookHandoff: hooks[name], RepoHandoff: repos.RoomHandoffs[name], LimitHandoff: warnings}
		b.Counts = roomBundleCounts(b)
		parts = append(parts, b)
	}
	rows, err := db.Query(`SELECT name,service FROM session_records WHERE closed=0 AND history_only=0 AND (service=1 OR json_extract(meta,'$.run') IS NOT NULL)`)
	if err != nil {
		return g, nil, report, err
	}
	for rows.Next() {
		var name string
		var service bool
		if err := rows.Scan(&name, &service); err != nil {
			rows.Close()
			return g, nil, report, err
		}
		if service {
			report.SkippedServices = append(report.SkippedServices, name)
		} else {
			report.SkippedWorkflowLeaves++
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return g, nil, report, err
	}
	rows.Close()
	return g, parts, report, nil
}

func writeReport(out string, report exportReport) error {
	f, err := os.OpenFile(filepath.Join(out, "report.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(report)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}
