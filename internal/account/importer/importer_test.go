package importer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
)

// fixture is one source store and one fake Pi host.
type fixture struct {
	store  *store.Store
	host   *fakeHost
	source string
}

// newFixture saves one Codex sign-in with one repository rule.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st := &store.Store{Root: filepath.Join(dir, "accounts")}
	var source string
	if err := st.Update(func(state *store.State) error {
		meta, err := state.Save("codex", "chatgpt", "openai:user:workspace", "Work", codexCredential(1_800_000_000))
		source = meta.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	scopes, err := reposcope.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := scopes.Set(source, []string{"github.com/org/name"}); err != nil {
		t.Fatal(err)
	}
	return &fixture{store: st, host: &fakeHost{store: st, source: source}, source: source}
}

// fakeHost records the order of the move and can stop any write step.
type fakeHost struct {
	store          *store.Store
	source         string
	calls          []string
	login          PiLogin
	sourceAtCreate bool
	failCreate     error
	failRepos      error
}

func (h *fakeHost) CreatePiAccount(_ context.Context, provider store.Provider, kind store.Kind, label string, login PiLogin) (PiAccount, error) {
	h.calls = append(h.calls, "create")
	if h.failCreate != nil {
		return PiAccount{}, h.failCreate
	}
	state, err := h.store.Read()
	if err != nil {
		return PiAccount{}, err
	}
	h.sourceAtCreate = state.Find(h.source) != nil
	h.login = login
	return PiAccount{ID: "pi-account", Provider: provider, Kind: kind}, nil
}

func (h *fakeHost) RemovePiAccount(_ context.Context, _ string) error {
	h.calls = append(h.calls, "remove-account")
	return nil
}

func (h *fakeHost) SetRepos(_ string, _ []string) error {
	h.calls = append(h.calls, "repos")
	return h.failRepos
}

func (h *fakeHost) RetireSource(_ context.Context, id string) error {
	h.calls = append(h.calls, "retire")
	return h.store.Update(func(state *store.State) error {
		if !state.Remove(id) {
			return errors.New("the source row is gone")
		}
		return nil
	})
}

// entry returns the saved source row, or nil.
func (f *fixture) entry(t *testing.T) *store.Entry {
	t.Helper()
	state, err := f.store.Read()
	if err != nil {
		t.Fatal(err)
	}
	return state.Find(f.source)
}

// TestImportMovesTheSignInOnlyAfterThePiAccountReadsBack pins the order: the
// source is still there while the Pi account is written, and retires last.
func TestImportMovesTheSignInOnlyAfterThePiAccountReadsBack(t *testing.T) {
	f := newFixture(t)
	im := &Importer{Store: f.store, Host: f.host}
	result, err := im.Run(context.Background(), Request{Harness: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.host.calls, []string{"create", "repos", "retire"}) {
		t.Fatalf("steps = %v", f.host.calls)
	}
	if !f.host.sourceAtCreate {
		t.Fatal("the source was gone before the Pi account was written")
	}
	if got := f.entry(t); got != nil {
		t.Fatalf("the source row stayed: %+v", got)
	}
	if f.host.login.EntryID != "openai-codex" || !reflect.DeepEqual(f.host.login.Repos, []string{"github.com/org/name"}) {
		t.Fatalf("Pi login = %+v", f.host.login)
	}
	want := []Moved{{Source: f.source, Label: "Work", Account: "pi-account", Provider: store.ProviderChatGPT, Kind: store.KindOAuth}}
	if !reflect.DeepEqual(result.Moved, want) {
		t.Fatalf("moved = %+v", result.Moved)
	}
}

// TestImportLeavesTheSourceOnAFailedWrite keeps the only copy when the Pi write
// fails.
func TestImportLeavesTheSourceOnAFailedWrite(t *testing.T) {
	f := newFixture(t)
	before, _ := f.store.Read()
	f.host.failCreate = errors.New("Pi's home is not writable")
	im := &Importer{Store: f.store, Host: f.host}
	if _, err := im.Run(context.Background(), Request{Harness: "codex"}); err == nil {
		t.Fatal("a failed write reported success")
	}
	after, _ := f.store.Read()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("a failed write changed the saved source")
	}
	if !reflect.DeepEqual(f.host.calls, []string{"create"}) {
		t.Fatalf("steps = %v", f.host.calls)
	}
}

// TestImportRollsBackWhenALaterStepFails removes the new Pi account, so the
// source is never one of two copies of the same refresh token.
func TestImportRollsBackWhenALaterStepFails(t *testing.T) {
	f := newFixture(t)
	f.host.failRepos = errors.New("the rules could not be copied")
	im := &Importer{Store: f.store, Host: f.host}
	if _, err := im.Run(context.Background(), Request{Harness: "codex"}); err == nil {
		t.Fatal("a failed step reported success")
	}
	if !reflect.DeepEqual(f.host.calls, []string{"create", "repos", "remove-account"}) {
		t.Fatalf("steps = %v", f.host.calls)
	}
	if f.entry(t) == nil {
		t.Fatal("the source row was retired")
	}
}

// TestImportRefusesAHarnessInUse names the enabled profile that keeps the
// harness, and imports the same harness once no enabled profile uses it.
func TestImportRefusesAHarnessInUse(t *testing.T) {
	f := newFixture(t)
	im := &Importer{Store: f.store, Host: f.host}
	use := Use{Profiles: []UseProfile{{Name: "sol", Harness: "codex"}}, Sessions: []UseSession{{Ref: "dyna:worker", Harness: "pi"}}}
	result, err := im.Run(context.Background(), Request{Harness: "codex", DryRun: true, Use: use})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Refused) != 1 || result.Refused[0].Reason != ReasonInUse || !reflect.DeepEqual(result.Refused[0].Profiles, []string{"sol"}) {
		t.Fatalf("refused = %+v", result.Refused)
	}
	if len(result.Moved) != 0 || len(f.host.calls) != 0 || f.entry(t) == nil {
		t.Fatal("a harness in use was moved")
	}
	if got := result.Refused[0].Describe("codex"); got == "" || got == "Dyna did not import codex: an enabled profile or an open session still runs on it ()." {
		t.Fatalf("refusal text = %q", got)
	}
	// The same harness with no enabled profile and no open session imports.
	result, err = im.Run(context.Background(), Request{Harness: "codex"})
	if err != nil || len(result.Moved) != 1 {
		t.Fatalf("import after the user left = %+v, %v", result, err)
	}
}

// TestImportKeepsAnOpenSessionAndADisabledProfileApart proves that only an
// enabled profile or an open session of the harness refuses it.
func TestImportKeepsAnOpenSessionAndADisabledProfileApart(t *testing.T) {
	f := newFixture(t)
	use := Use{Sessions: []UseSession{{Ref: "dyna:worker", Harness: "codex"}}}
	im := &Importer{Store: f.store, Host: f.host}
	result, err := im.Run(context.Background(), Request{Harness: "codex", DryRun: true, Use: use})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Refused) != 1 || !reflect.DeepEqual(result.Refused[0].Sessions, []string{"dyna:worker"}) || len(result.Moved) != 0 {
		t.Fatalf("an open session did not refuse the harness: %+v", result)
	}
}

// TestDryRunListsTheMoveWithoutWriting reports what would move and changes
// nothing, including for one named account.
func TestDryRunListsTheMoveWithoutWriting(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Update(func(state *store.State) error {
		_, err := state.Save("codex", "chatgpt", "openai:other:workspace", "Home", codexCredential(1_800_000_001))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	im := &Importer{Store: f.store, Host: f.host}
	result, err := im.Run(context.Background(), Request{Harness: "codex", Account: "Home", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Moved) != 1 || result.Moved[0].Label != "Home" || result.Moved[0].Account != "" {
		t.Fatalf("moved = %+v", result.Moved)
	}
	if len(f.host.calls) != 0 {
		t.Fatalf("a dry run wrote: %v", f.host.calls)
	}
	state, _ := f.store.Read()
	if len(state.Entries) != 2 {
		t.Fatalf("a dry run retired a row: %d left", len(state.Entries))
	}
	raw, _ := json.Marshal(result)
	if string(raw) == "" || !json.Valid(raw) {
		t.Fatalf("result is not JSON: %s", raw)
	}
}
