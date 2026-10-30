package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/database"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/room"
	"dyna-agent/internal/updaterservice"
)

// qaSeedFixtures writes only to an existing, empty data directory. The caller
// must create that directory for the isolated QA engine and remove it afterward.
func qaSeedFixtures(dataDir string) error {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return fmt.Errorf("read QA data directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("QA data directory must be empty")
	}
	rooms := &room.Store{Root: filepath.Join(dataDir, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "qa-demo", CreatedBy: "owner"})
	if err != nil {
		return err
	}
	for _, post := range []room.PostInput{
		{From: "owner", Human: true, To: []string{rec.Name}, Text: "Please check the sample change."},
		{From: rec.Name + ":root", To: []string{rec.Name}, Text: "The sample change is ready for review."},
	} {
		if _, err := rooms.Post(rec.ID, post); err != nil {
			return err
		}
	}
	goalStore := &goals.Store{Root: filepath.Join(dataDir, "goals")}
	goal, err := goalStore.Add(rec.ID, "owner", goals.Input{Text: "Review the sample change", Shared: true})
	if err != nil {
		return err
	}
	const key = "github.com/example/widgets#7"
	pr := &state.Record{Key: key, Host: "github.com", Repo: "example/widgets", Number: 7,
		URL: "https://github.com/example/widgets/pull/7", Title: "Improve the sample page",
		RoomID: rec.ID, GoalID: goal.ID, State: state.PROpen, Branch: "sample-change", Base: "main"}
	if err := state.Write(state.Root(dataDir), state.State{Version: 1, Records: map[string]*state.Record{key: pr}}); err != nil {
		return err
	}
	// Saved, synthetic readings let Home show a full row of provider cards.
	// They contain no credentials and never contact a provider.
	accounts := &accountstore.Store{Root: filepath.Join(dataDir, "accounts")}
	providers := []accountstore.Provider{accountstore.ProviderAnthropic, accountstore.ProviderChatGPT, accountstore.ProviderKimiCode, accountstore.ProviderXAI, accountstore.ProviderDeepSeek, accountstore.ProviderZAI}
	used := 35.0
	now := time.Now().UTC()
	if err := accounts.Update(func(state *accountstore.State) error {
		for i, provider := range providers {
			id := fmt.Sprintf("%032x", i+1)
			state.Entries = append(state.Entries, accountstore.Entry{Metadata: accountstore.Metadata{
				ID: id, Harness: "pi", Provider: provider, Kind: accountstore.KindAPIKey,
				Identity: "qa:" + string(provider), Label: string(provider),
				NativeHome:  filepath.Join(accounts.Root, "pi", id),
				Observation: &accountstore.Observation{ObservedAt: now, Windows: []accountstore.Window{{ID: "weekly", Label: "Weekly", UsedPercent: &used}}},
			}})
		}
		return nil
	}); err != nil {
		return err
	}
	for i, provider := range providers {
		if err := os.MkdirAll(filepath.Join(accounts.Root, "pi", fmt.Sprintf("%032x", i+1)), 0700); err != nil {
			return err
		}
		if err := accounts.SetActive(provider, "pi", "", fmt.Sprintf("%032x", i+1)); err != nil {
			return err
		}
	}
	if err := watch.Migrate(dataDir); err != nil {
		return err
	}
	db, err := database.Open(dataDir)
	if err != nil {
		return err
	}
	entry := watch.MergeHistoryEntry{Kind: "publish", Outcome: "published", Repo: "github.com/example/widgets", Branch: "dyna/merge-1", Head: "1234567890abcdef", Changes: []string{"#7 Improve the sample page"}, At: now}
	body, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	roomIDs, err := json.Marshal([]string{rec.ID})
	if err != nil {
		return err
	}
	if _, err := db.Exec(`INSERT INTO merge_history(repo, rooms, kind, outcome, branch, head, body, at) VALUES(?,?,?,?,?,?,?,?)`, entry.Repo, string(roomIDs), entry.Kind, entry.Outcome, entry.Branch, entry.Head, string(body), now.UnixNano()); err != nil {
		return err
	}
	// A finished installer receipt appears in the Updates service activity.
	// Writing a receipt does not start an installer or a service.
	const id = "00000000000000000000000000000001"
	root := filepath.Join(dataDir, "hooks", "install")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	logPath := filepath.Join(root, "qa-update.log")
	if err := os.WriteFile(logPath, []byte("Sample update completed.\n"), 0600); err != nil {
		return err
	}
	install := hookinstall.Store{Root: root}
	return install.Update(id, func(job *hookinstall.Job) error {
		*job = hookinstall.Job{ID: id, Hook: updaterservice.RunName, HookLog: logPath,
			Status: "finished", Result: "pass", Recorded: true,
			StartedAt: now.Add(-time.Second), FinishedAt: now}
		return nil
	})
}
