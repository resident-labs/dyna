package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	prstate "dyna-agent/internal/pr/state"
	"github.com/resident-labs/dyna/contract"
)

type skippedPR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func readPRs(db *sql.DB, room string, author func(string) (contract.ImportedAuthor, error)) ([]contract.TrackedPR, []skippedPR, error) {
	rows, err := db.Query("SELECT repo,number,body FROM pr_records WHERE room=? AND closed=0 ORDER BY repo,number", room)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var result []contract.TrackedPR
	var skipped []skippedPR
	for rows.Next() {
		var repo string
		var number int
		var body []byte
		if err := rows.Scan(&repo, &number, &body); err != nil {
			return nil, nil, err
		}
		var doc struct {
			Record *prstate.Record `json:"record"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return nil, nil, fmt.Errorf("decode tracked PR: %w", err)
		}
		if doc.Record == nil {
			return nil, nil, fmt.Errorf("tracked PR has no record")
		}
		rec := doc.Record
		if !strings.EqualFold(rec.Host, "github.com") {
			skipped = append(skipped, skippedPR{Repo: rec.Host + "/" + repo, Number: number})
			continue
		}
		owner := rec.Author
		if owner == "" {
			owner = "owner"
		}
		by, err := author(owner)
		if err != nil {
			return nil, nil, err
		}
		result = append(result, contract.TrackedPR{Branch: rec.Branch, Repo: contract.RepoName(strings.TrimSuffix(repo, ".git")), Number: number, Author: by, Goal: contract.GoalID(rec.GoalID)})
	}
	return result, skipped, rows.Err()
}
