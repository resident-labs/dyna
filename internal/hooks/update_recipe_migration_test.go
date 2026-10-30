package hooks

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/shellquote"
)

func TestMigrateUpdateRecipe(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(string(rune('0'+count)), func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() { database.Close(dir) })
			if err := MigrateDocuments(dir); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			list := []Hook{{Name: "update", Label: "Update Dyna", Command: "'/bin/dyna' git hooks _install --main '/source' --name 'update' --tool-path '/bin'", EveryMinutes: 17, Disabled: true}, {Name: "custom", Label: "Update Dyna", Command: "echo custom"}}
			if count == 2 {
				list = append(list, Hook{Name: "other", Label: "Update Dyna", Command: "'/bin/dyna' git hooks _install --main '/other' --name 'other' --tool-path '/bin'"})
			}
			body, err := json.Marshal(document{Repo: "/source", Hooks: list})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO hook_documents(repo,body) VALUES('repo',?)`, body); err != nil {
				t.Fatal(err)
			}
			if err := MigrateUpdateRecipe(dir); err != nil {
				t.Fatal(err)
			}
			var source string
			var enabled, every, multiple int
			if err := db.QueryRow(`SELECT source,enabled,every_minutes,multiple_hooks FROM updater_service`).Scan(&source, &enabled, &every, &multiple); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := db.QueryRow(`SELECT body FROM hook_documents WHERE repo='repo'`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var got document
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if count == 1 && (source != "/source" || enabled != 0 || every != 17 || multiple != 0 || len(got.Hooks) != 1 || got.Hooks[0].Command != "echo custom") {
				t.Fatalf("single: %q %d %d %d %#v", source, enabled, every, multiple, got.Hooks)
			}
			if count == 2 && (source != "" || multiple != 1 || len(got.Hooks) != 3) {
				t.Fatalf("multiple: %q %d %#v", source, multiple, got.Hooks)
			}
		})
	}
}

func TestUpdateRecipeSourcePreservesEditedCommand(t *testing.T) {
	source := "/checkout owner's dyna"
	h := Hook{Name: "update", Label: "Update Dyna", Command: shellquote.Quote("/bin/dyna") + " git hooks _install --main " + shellquote.Quote(source) + " --name 'update' --tool-path '/bin:/usr/bin'"}
	if got, ok := updateRecipeSource(h); !ok || got != source {
		t.Fatalf("recipe source = %q, %v", got, ok)
	}
	h.Command += "; echo owner-edit"
	if _, ok := updateRecipeSource(h); ok {
		t.Fatal("edited command matched recipe")
	}
}
