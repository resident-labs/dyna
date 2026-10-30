package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeliveryQueueMigration(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.Replace(schema, ", queue TEXT, PRIMARY KEY(to_kind, to_id, seq)", ", PRIMARY KEY(to_kind, to_id, seq)", 1)
	for _, line := range strings.Split(schema, "\n") {
		if strings.HasPrefix(line, "CREATE TABLE repos ") || strings.HasPrefix(line, "CREATE TABLE room_repos ") || strings.HasPrefix(line, "CREATE UNIQUE INDEX room_repos_default ") {
			oldSchema = strings.Replace(oldSchema, line+"\n", "", 1)
		}
	}
	if _, err = db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO meta(key,value) VALUES ('format','dyna-v2'),('schema_version','1'),('store_id','test'),('created_commit','')"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir, func() time.Time { return time.Unix(1000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
}
