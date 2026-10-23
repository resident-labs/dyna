package state

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestLegacyPauseDoesNotBlockUnrelatedAccount(t *testing.T) {
	root := newRoot(t)
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	raw := `{"legacyQuotaPause":"2036-10-21T12:00:00Z","rateLimits":{"github.com\nalice\ncore":{"pauseUntil":"2036-10-21T18:00:00Z","remaining":17,"future":9007199254740993}}}`
	if _, err = db.Exec("INSERT INTO pr_state VALUES(1,1,?)", raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO pr_records VALUES('a','','',0,0,1,?)", `{"record":{"host":"github.com","quotaAccount":"alice"}}`); err != nil {
		t.Fatal(err)
	}
	st, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2036, 10, 21, 10, 0, 0, 0, time.UTC)
	if !st.Quota("github.com", "bob", now).PauseUntil.IsZero() {
		t.Fatal("unrelated account is paused")
	}
	if !st.Quota("github.com", "alice", now).PauseUntil.Equal(now.Add(8 * time.Hour)) {
		t.Fatal("longer scoped pause lost")
	}
	if !st.RateLimits[(RateLimitScope{"github.com", "alice", "graphql"}).Key()].PauseUntil.Equal(now.Add(2 * time.Hour)) {
		t.Fatal("recorded account lacks graphql pause")
	}
	var body string
	if err = db.QueryRow("SELECT body FROM pr_state").Scan(&body); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["legacyQuotaPause"]; ok {
		t.Fatal("global pause remains")
	}
	var rates map[string]map[string]json.RawMessage
	if err = json.Unmarshal(doc["rateLimits"], &rates); err != nil {
		t.Fatal(err)
	}
	core := rates[(RateLimitScope{"github.com", "alice", "core"}).Key()]
	if string(core["future"]) != "9007199254740993" || string(core["remaining"]) != "17" {
		t.Fatal("opaque accounting changed")
	}
	if err = Migrate(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	var replay string
	if err = db.QueryRow("SELECT body FROM pr_state").Scan(&replay); err != nil {
		t.Fatal(err)
	}
	if replay != body {
		t.Fatal("replay changed state")
	}
}
