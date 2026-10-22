package computers

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"dyna-agent/internal/store"
)

// RecordFailure keeps setup and cleanup failures visible without failing an
// unrelated agent turn or the rest of a durable session-close transaction.
func RecordFailure(id string, err error) {
	if err == nil {
		return
	}
	if os.MkdirAll(Root(), 0700) != nil {
		return
	}
	if f, e := os.OpenFile(filepath.Join(Root(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); e == nil {
		fmt.Fprintf(f, "%s computer %s: %v\n", time.Now().Format(time.RFC3339), id, err)
		f.Close()
	}
	if ValidateID(id) != nil {
		return
	}
	if os.MkdirAll(Dir(id), 0700) != nil {
		return
	}
	_ = store.WriteJSON(filepath.Join(Dir(id), "failure.json"), map[string]string{"reason": err.Error(), "at": time.Now().Format(time.RFC3339)}, 0o600)
}
func failureReason(id string) string {
	var v struct {
		Reason string `json:"reason"`
	}
	b, err := os.ReadFile(filepath.Join(Dir(id), "failure.json"))
	if err == nil {
		_ = json.Unmarshal(b, &v)
	}
	return v.Reason
}
