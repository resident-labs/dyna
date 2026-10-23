package sessionlife

import (
	"context"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestStartupPersistsRetainedServiceClassification(t *testing.T) {
	data := t.TempDir()
	t.Cleanup(func() { _ = database.Close(data) })
	sessions := filepath.Join(data, "sessions")
	cases := []struct {
		id, role, parent, room string
		stored, want           bool
	}{
		{"reviewer", "reviewer", "", "a", false, true},
		{"helper", "", "reviewer", "a", false, true},
		{"grandchild", "", "helper", "a", false, true},
		{"root", "root", "", "a", false, true},
		{"root-helper", "", "root", "a", false, false},
		{"foreign", "", "reviewer", "b", false, false},
		{"missing", "", "absent", "a", false, false},
		{"cycle-a", "", "cycle-b", "a", false, false},
		{"cycle-b", "", "cycle-a", "a", false, false},
		{"explicit", "", "", "a", true, true},
		{"explicit-helper", "", "explicit", "a", false, true},
		{"keeper", "keeper", "", "a", false, true},
		{"library", "librarian", "", "a", false, true},
		{"steward", "merge-steward", "", "a", false, true},
	}
	for _, c := range cases {
		if err := Save(sessions, c.id, map[string]any{"id": c.id, "name": c.id, "status": "idle", "role": c.role, "parentSession": c.parent, "roomId": c.room, "room": c.room, "service": c.stored, "custom": "preserved"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	versions := map[string]Version{}
	for _, c := range cases {
		var got struct {
			Service bool
			Custom  string
		}
		version, err := LoadStoredVersion(context.Background(), db, c.id, &got)
		if err != nil {
			t.Fatal(err)
		}
		if got.Service != c.want || got.Custom != "preserved" {
			t.Errorf("%s: got %+v, want service %t", c.id, got, c.want)
		}
		versions[c.id] = version
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		var got struct{ Service bool }
		version, err := LoadStoredVersion(context.Background(), db, c.id, &got)
		if err != nil {
			t.Fatal(err)
		}
		if version != versions[c.id] || got.Service != c.want {
			t.Errorf("%s changed on replay", c.id)
		}
	}
}
