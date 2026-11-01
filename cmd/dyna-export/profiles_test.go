package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestUnsupportedMuseWarningStaysWithAffectedSession(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE session_records(id TEXT,name TEXT,parent TEXT,meta TEXT,room_id TEXT,closed INTEGER,history_only INTEGER,service INTEGER);
CREATE TABLE goals(room TEXT,id TEXT,state TEXT,body TEXT);
INSERT INTO session_records VALUES ('s1','one','','{"profile":"wonder"}','r',0,0,0),('s2','two','','{"profile":"opus"}','r',0,0,0);`)
	if err != nil {
		t.Fatal(err)
	}
	author := func(string) (contract.ImportedAuthor, error) { return contract.ImportedAuthor{}, nil }
	got, err := readHandoff(db, "r", "alpha", roomFeed{}, nil, []profileGap{{Profile: "wonder", Model: "meta/muse-wonder-full-1.0", Reason: contract.ProfileUnsupportedMuse}}, author)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ProfileWarning != contract.ProfileUnsupportedMuse || got[1].ProfileWarning != "" {
		t.Fatalf("warnings = %+v", got)
	}
}

func TestProfileMappingPreservesThinkingAndReportsUnsupported(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":3,"profiles":[
{"name":"rewrite","model":"opus","maxCallsPerRun":8,"extraArgs":["--effort","xhigh"]},
{"name":"terra","model":"openai-codex/gpt-6.1-terra","extraArgs":["--thinking=high"]},
{"name":"wonder","model":"meta/muse-wonder-full-1.0"},
{"name":"disabled","model":"unused","disabled":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, disabled, unknown, dropped, err := readProfiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("maxCallsPerRun")) {
		t.Fatalf("obsolete limit in exported profiles: %s", encoded)
	}
	if len(profiles) != 2 || profiles[0].Model != "claude-opus-5-5" || profiles[0].Thinking != contract.ThinkingXHigh || profiles[1].Model != "gpt-6.1-terra" || profiles[1].Thinking != contract.ThinkingHigh || len(disabled) != 1 || len(unknown) != 0 || len(dropped) != 1 {
		t.Fatalf("profiles=%+v disabled=%+v unknown=%+v", profiles, disabled, unknown)
	}
}
