package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"dyna-agent/internal/room"
	"github.com/resident-labs/dyna/contract"
)

func readIssues(db *sql.DB, roomID string, feed roomFeed, author func(string) (contract.ImportedAuthor, error)) ([]contract.IssueBundle, error) {
	rows, err := db.Query(`SELECT i.body,COALESCE(n.number,0) FROM room_issues i
LEFT JOIN room_issue_numbers n ON n.room_id=i.room_id AND n.message_id=i.id
WHERE i.room_id=? AND i.state IN ('open','goal') ORDER BY i.seq`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []contract.IssueBundle
	for rows.Next() {
		var body []byte
		var number int
		if err := rows.Scan(&body, &number); err != nil {
			return nil, err
		}
		var issue room.Issue
		if err := json.Unmarshal(body, &issue); err != nil {
			return nil, fmt.Errorf("decode issue: %w", err)
		}
		if feed.legacyIssueRefs[issue.ID] {
			return nil, fmt.Errorf("selected issue has a legacy feed event without a typed payload")
		}
		events, ok := feed.issues[issue.ID]
		if !ok || events.filed.Type != room.IssueFiled || number < 1 {
			return nil, fmt.Errorf("issue is missing its filing event or number")
		}
		filer, err := author(issue.Filer)
		if err != nil {
			return nil, err
		}
		out := contract.IssueBundle{Number: number, Title: issue.Title, Body: events.filed.Detail,
			Filer: filer, Filed: time.UnixMilli(issue.FiledAt).UTC(), Goal: contract.GoalID(issue.GoalID), GitHubURL: issue.GitHubURL}
		if issue.Assignee != "" {
			a, err := author(issue.Assignee)
			if err != nil {
				return nil, err
			}
			out.Assignee = a.Label
		}
		if len(events.filed.Files) == 0 && issue.File != "" {
			out.Files = append(out.Files, contract.IssueFile{Path: issue.File, Line: issue.Line})
		}
		for _, f := range events.filed.Files {
			out.Files = append(out.Files, contract.IssueFile{Path: f.Path, Line: f.Line})
		}
		for _, post := range events.notes {
			event, _ := post.Issue()
			by, err := author(string(event.Actor))
			if err != nil {
				return nil, err
			}
			out.Notes = append(out.Notes, contract.IssueNote{At: time.UnixMilli(post.TS).UTC(), By: by, Text: event.Detail})
		}
		result = append(result, out)
	}
	return result, rows.Err()
}
