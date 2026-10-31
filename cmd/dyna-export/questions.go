package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"dyna-agent/internal/questions"
	"github.com/resident-labs/dyna/contract"
)

func readQuestions(db *sql.DB, room string, author func(string) (contract.ImportedAuthor, error)) ([]contract.QuestionBundle, error) {
	rows, err := db.Query("SELECT body FROM questions WHERE room=? AND state='open' ORDER BY key", room)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []contract.QuestionBundle
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var q questions.Question
		if err := json.Unmarshal(body, &q); err != nil {
			return nil, fmt.Errorf("decode question: %w", err)
		}
		by, err := author(q.Asker)
		if err != nil {
			return nil, err
		}
		out := contract.QuestionBundle{
			Label: contract.QuestionLabel(q.ID), Kind: contract.QuestionAsk,
			Asker: by, Text: q.Text, Options: q.Options, Meanwhile: q.Meanwhile, Asked: q.AskedAt,
		}
		if q.Kind == questions.KindTerminal {
			if q.Terminal == nil {
				return nil, fmt.Errorf("terminal question has no terminal request")
			}
			out.Kind = contract.QuestionTerminal
			out.Argv, out.Cwd, out.Reason = q.Terminal.Argv, q.Terminal.Cwd, q.Terminal.Reason
			quoted := make([]string, len(out.Argv))
			for i, arg := range out.Argv {
				quoted[i] = strconv.Quote(arg)
			}
			out.Command = strings.Join(quoted, " ")
		} else if q.Kind != "" && q.Kind != questions.KindQuestion {
			return nil, fmt.Errorf("unknown question kind %q", q.Kind)
		}
		result = append(result, out)
	}
	return result, rows.Err()
}
