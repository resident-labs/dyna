package state

import (
	"dyna-agent/internal/database"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyMigrationPreservesRealChecksAndHistory(t *testing.T) {
	root := newRoot(t)
	db, err := database.Open(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	policy := `{"land":"branch-only","checks":"local","requiredChecks":["go-test-touched","unit","frontend-if-touched"],"opaque":9007199254740993}`
	if _, err := db.Exec(`INSERT INTO pr_state VALUES(1,41,?)`, `{"policies":{"r":`+policy+`},"future":7}`); err != nil {
		t.Fatal(err)
	}
	body := `{"record":{"key":"one","policyOverride":{"land":"local-merge","checks":"local","requiredChecks":[]},"reviewHistory":[{"policy":` + policy + `}]},"opaque":9}`
	if _, err := db.Exec(`INSERT INTO pr_records VALUES('one','r','repo',1,0,41,?)`, body); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	var index, got string
	var rev int
	if err := db.QueryRow("SELECT body,revision FROM pr_state").Scan(&index, &rev); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(index, `"land"`) || strings.Contains(index, `"checks"`) || !strings.Contains(index, `"requiredChecks":["unit"]`) || !strings.Contains(index, "9007199254740993") || rev != 43 {
		t.Fatal(index, rev)
	}
	got = joinedBody(t, db, "one")
	if !strings.Contains(got, `"policyOverride":{"requiredChecks":[]}`) || !strings.Contains(got, policy) || !strings.Contains(got, `"opaque":9`) {
		t.Fatal(got)
	}
	if err := Migrate(filepath.Dir(root)); err != nil {
		t.Fatal(err)
	}
	replay := joinedBody(t, db, "one")
	if replay != got {
		t.Fatal("replay changed row")
	}
}
