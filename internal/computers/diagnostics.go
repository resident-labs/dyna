package computers

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecordFailure keeps setup and cleanup failures visible without failing an
// unrelated agent turn or the rest of a durable session-close transaction.
// The daemon log keeps the detail; the reason itself lives in dyna.db.
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
	_ = saveFailure(id, err.Error())
}

// clearFailedSetup forgets a recorded failure once the computer runs again,
// and removes the failure.json that an earlier release left in its folder.
func clearFailedSetup(id string) {
	_ = clearFailure(id)
	_ = os.Remove(filepath.Join(Dir(id), "failure.json"))
}
