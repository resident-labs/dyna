package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/profile"
	"github.com/resident-labs/dyna/contract"
)

// File counts come from the captured catalog, independently of export.
func verifyCapturedFiles(data, config, bundle string, cap capture) error {
	f, err := os.Open(filepath.Join(bundle, "global.json"))
	if err != nil {
		return err
	}
	global, err := contract.DecodeBundle[contract.GlobalBundle](f)
	f.Close()
	if err != nil {
		return err
	}
	body, err := os.ReadFile(filepath.Join(config, "profiles.json"))
	if err != nil {
		return err
	}
	var profiles profile.Store
	if err := json.Unmarshal(body, &profiles); err != nil {
		return err
	}
	known := map[string]bool{
		"anthropic/claude-opus-5-5": true, "claude-opus-5-5": true,
		"anthropic/claude-sonnet-5-5": true, "claude-sonnet-5-5": true,
		"anthropic/claude-haiku-4-5-20251001": true, "claude-haiku-4-5-20251001": true,
	}
	n := 0
	for _, p := range profiles.Profiles {
		if p.Disabled {
			continue
		}
		models := p.Models
		if len(models) == 0 {
			models = []string{p.Model}
		}
		valid := true
		for _, model := range models {
			if !known[model] {
				valid = false
			}
		}
		if valid {
			n++
		}
	}
	if n != len(global.Profiles) {
		return fmt.Errorf("captured profile count differs")
	}
	if len(cap.Skills) != len(global.Skills) {
		return fmt.Errorf("captured skill count differs")
	}
	root, err := os.OpenRoot(data)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, skill := range cap.Skills {
		if skill.Path != "" {
			f, err := root.Open(skill.Path)
			if err != nil {
				return fmt.Errorf("captured skill missing: %w", err)
			}
			f.Close()
		}
	}
	if _, err := os.Stat(filepath.Join(config, "settings.json")); err != nil {
		return err
	}
	return nil
}

// countRepositories decodes only room repository paths and counts their
// distinct supported identities independently of the bundle writer.
func countRepositories(db *sql.DB, cap capture) (int, error) {
	rows, err := db.Query("SELECT body FROM room_records")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	seen := make(map[contract.RepoName]bool)
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return 0, err
		}
		var rec struct {
			Repo       string   `json:"repo"`
			ExtraRepos []string `json:"extraRepos"`
		}
		if err := json.Unmarshal(body, &rec); err != nil {
			return 0, err
		}
		if rec.Repo != "" {
			rec.ExtraRepos = append(rec.ExtraRepos, rec.Repo)
		}
		for _, p := range rec.ExtraRepos {
			id := cap.Repos[p]
			if repo, ok := githubRepo(string(id)); ok {
				seen[repo] = true
			}
		}
	}
	return len(seen), rows.Err()
}
