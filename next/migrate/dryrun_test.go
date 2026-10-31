package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "rooms"), 0700); err != nil {
		t.Fatal(err)
	}
	global := contract.GlobalBundle{Version: contract.BundleV1, Rooms: []contract.RoomName{"alpha"}, Profiles: []contract.ProfileBundle{{Model: "model-a", Models: []string{"model-a", "model-b"}}}, Repos: []contract.RepoBundle{{}}, Skills: []contract.SkillBundle{{}}}
	room := contract.RoomBundle{Version: contract.BundleV1, Name: "alpha", Goals: []contract.GoalBundle{{Items: []contract.GoalItemBundle{{}}}}, Counts: contract.BundleCounts{Goals: 1, GoalItems: 1}}
	globalData, err := json.Marshal(global)
	if err != nil {
		t.Fatal(err)
	}
	roomData, err := json.Marshal(room)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"global.json": globalData, "rooms/alpha.json": roomData} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDryRun(t *testing.T) {
	dir := fixture(t)
	report, err := DryRun(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ModelsCheckedAtImport) != 2 || report.ModelsCheckedAtImport[0] != "model-a" || report.ModelsCheckedAtImport[1] != "model-b" {
		t.Fatal("models are not listed for the import check")
	}
	if report.Profiles != 1 || report.Repositories != 1 || report.Skills != 1 {
		t.Fatalf("global record counts: %+v", report)
	}
	if got := report.Rooms["alpha"]; got.Goals != 1 || got.GoalItems != 1 {
		t.Fatalf("counts: %+v", got)
	}
}

func TestDryRunRefusesInvalidBundle(t *testing.T) {
	for _, tc := range []struct{ name, file, old, replacement string }{
		{"version", "global.json", `"version":1`, `"version":2`},
		{"unknown field", "rooms/alpha.json", `"version":1`, `"version":1,"extra":true`},
		{"count", "rooms/alpha.json", `"goalItems":1`, `"goalItems":2`},
		{"room name", "global.json", `"alpha"`, `"../evil"`},
		{"room mismatch", "rooms/alpha.json", `"name":"alpha"`, `"name":"other"`},
		{"duplicate room", "global.json", `"rooms":["alpha"]`, `"rooms":["alpha","alpha"]`},
		{"missing manifest", "global.json", `"rooms":["alpha"]`, `"rooms":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture(t)
			path := filepath.Join(dir, tc.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(string(data), tc.old, tc.replacement, 1)
			if changed == string(data) {
				t.Fatal("fixture did not change")
			}
			if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := DryRun(dir); err == nil {
				t.Fatal("accepted invalid bundle")
			}
		})
	}
}

func TestDryRunRefusesMissingAndExtraFiles(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		dir := fixture(t)
		if err := os.Remove(filepath.Join(dir, "rooms/alpha.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := DryRun(dir); err == nil {
			t.Fatal("accepted missing part")
		}
	})
	t.Run("extra", func(t *testing.T) {
		dir := fixture(t)
		if err := os.WriteFile(filepath.Join(dir, "rooms/extra.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := DryRun(dir); err == nil {
			t.Fatal("accepted extra part")
		}
	})
}
