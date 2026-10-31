package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/resident-labs/dyna/contract"
)

var accountProviders = map[string]contract.AccountProvider{
	"anthropic":   contract.ProviderAnthropic,
	"chatgpt":     contract.ProviderOpenAI,
	"kimi-code":   contract.ProviderKimiCoding,
	"xai":         contract.ProviderXAI,
	"zai":         contract.ProviderZAI,
	"opencode-go": contract.ProviderOpencodeGo,
	"deepseek":    contract.ProviderDeepSeek,
}

// Only metadata is selected. Credential schemas and payloads are not read.
func readAccounts(db *sql.DB, rooms map[string]contract.RoomName, repoName func(string) (contract.RepoName, error)) ([]contract.AccountLabel, []string, error) {
	rows, err := db.Query("SELECT id,metadata FROM accounts ORDER BY ord")
	if err != nil {
		return nil, nil, err
	}
	type account struct {
		id, provider, label, room string
	}
	var source []account
	for rows.Next() {
		var id string
		var body []byte
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var m struct {
			Provider string `json:"provider"`
			Label    string `json:"label"`
			RoomID   string `json:"roomId"`
		}
		if err := json.Unmarshal(body, &m); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("decode account metadata: %w", err)
		}
		source = append(source, account{id, m.Provider, m.Label, m.RoomID})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()
	var result []contract.AccountLabel
	var skipped []string
	for _, a := range source {
		provider, ok := accountProviders[a.provider]
		if !ok {
			skipped = append(skipped, a.provider)
			continue
		}
		out := contract.AccountLabel{Provider: provider, Label: a.label}
		if a.room != "" {
			room, ok := rooms[a.room]
			if !ok {
				return nil, nil, fmt.Errorf("account refers to unknown room")
			}
			out.Rooms = []contract.RoomName{room}
		}
		scopes, err := db.Query("SELECT repo FROM account_repo_scopes WHERE account_id=? ORDER BY repo", a.id)
		if err != nil {
			return nil, nil, err
		}
		unsupported := false
		for scopes.Next() {
			var path string
			if err := scopes.Scan(&path); err != nil {
				scopes.Close()
				return nil, nil, err
			}
			repo, err := repoName(path)
			if errors.Is(err, errUnsupportedForge) {
				unsupported = true
				continue
			}
			if err != nil {
				scopes.Close()
				return nil, nil, err
			}
			out.Repos = append(out.Repos, repo)
		}
		if err := scopes.Err(); err != nil {
			scopes.Close()
			return nil, nil, err
		}
		scopes.Close()
		if unsupported {
			skipped = append(skipped, a.label)
			continue
		}
		result = append(result, out)
	}
	return result, skipped, nil
}
