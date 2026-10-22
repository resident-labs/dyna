package state

import (
	"fmt"
	"strings"
)

// IssueWork preserves the last session assigned to an issue, including after it ends.
type IssueWork struct {
	RoomID  string `json:"roomId"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	Session string `json:"session"`
}

func IssueWorkKey(roomID, repo string, number int) string {
	return fmt.Sprintf("%s/%s#%d", roomID, strings.ToLower(repo), number)
}
