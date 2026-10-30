package hooks

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/shellquote"
)

func TestMigrateUpdateRecipe(t *testing.T) {
	for _, tc := range []struct {
		name     string
		count    int
		disabled bool
	}{
		{"custom command only", 0, false},
		{"modified event", -1, false},
		{"single disabled", 1, true},
		{"release install with source opt-in", 1, false},
		{"multiple recipes", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := tc.count
			dir := t.TempDir()
			t.Cleanup(func() { database.Close(dir) })
			if err := MigrateDocuments(dir); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			list := []Hook{{Name: "update", Label: "Update Dyna", Command: "'/bin/dyna' git hooks _install --main '/source' --name 'update' --tool-path '/bin'", Event: MainUpdated, Cwd: "main", Timeout: "1m", OnFail: "alert", EveryMinutes: 17, Disabled: tc.disabled}, {Name: "custom", Label: "Update Dyna", Command: "echo custom"}}
			if count == -1 {
				list = list[:1]
				list[0].Event = SessionFinish
			}
			if count == 0 {
				list = list[1:]
			}
			if count == 2 {
				list = append(list, Hook{Name: "other", Label: "Update Dyna", Event: MainUpdated, Cwd: "main", Timeout: "1m", OnFail: "alert", Command: "'/bin/dyna' git hooks _install --main '/other' --name 'other' --tool-path '/bin'"})
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
			var source, tools string
			var enabled, every, multiple int
			if err := db.QueryRow(`SELECT source,enabled,every_minutes,multiple_hooks,tool_path FROM updater_service`).Scan(&source, &enabled, &every, &multiple, &tools); err != nil {
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
			wantEnabled := 1
			if tc.disabled {
				wantEnabled = 0
			}
			if count == 1 && (source != "/source" || enabled != wantEnabled || every != 17 || multiple != 0 || tools != "/bin" || len(got.Hooks) != 1 || got.Hooks[0].Command != "echo custom") {
				t.Fatalf("single: %q %d %d %d %#v", source, enabled, every, multiple, got.Hooks)
			}
			if count == -1 && (source != "" || enabled != 0 || len(got.Hooks) != 1 || got.Hooks[0].Event != SessionFinish) {
				t.Fatalf("modified event: %q %d %#v", source, enabled, got.Hooks)
			}
			if count == 0 && (source != "" || enabled != 0 || multiple != 0 || len(got.Hooks) != 1 || got.Hooks[0].Command != "echo custom") {
				t.Fatalf("custom: %q %d %d %#v", source, enabled, multiple, got.Hooks)
			}
			if count == 2 && (source != "" || multiple != 1 || len(got.Hooks) != 3) {
				t.Fatalf("multiple: %q %d %#v", source, multiple, got.Hooks)
			}
		})
	}
}

func TestUpdateRecipeSourcePreservesEditedHooks(t *testing.T) {
	source := "/checkout owner's dyna"
	h := Hook{Name: "update", Label: "Update Dyna", Event: MainUpdated, Cwd: "main", Timeout: "1m", OnFail: "alert", Command: shellquote.Quote("/bin/dyna") + " git hooks _install --main " + shellquote.Quote(source) + " --name 'update' --tool-path '/bin:/usr/bin'"}
	if got, _, ok := updateRecipeSource(h); !ok || got != source {
		t.Fatalf("recipe source = %q, %v", got, ok)
	}
	edits := []struct {
		name  string
		apply func(*Hook)
	}{
		{"command", func(h *Hook) { h.Command += "; echo owner-edit" }},
		{"event", func(h *Hook) { h.Event = SessionFinish }},
		{"directory", func(h *Hook) { h.Cwd = "worktree" }},
		{"timeout", func(h *Hook) { h.Timeout = "2m" }},
		{"failure policy", func(h *Hook) { h.OnFail = "block" }},
	}
	for _, edit := range edits {
		t.Run(edit.name, func(t *testing.T) {
			changed := h
			edit.apply(&changed)
			if _, _, ok := updateRecipeSource(changed); ok {
				t.Fatal("edited hook matched recipe")
			}
		})
	}
}
