package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	accountservice "dyna-agent/internal/account/service"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/websvc"
)

type museQuotaFixture struct {
	t      *testing.T
	db     *sql.DB
	store  *accountstore.Store
	client *engineclient.Client
	input  callspec.MuseQuotaObservation
	runRef callspec.ExecutionRef
	mu     sync.Mutex
	caller websvc.Caller
	drop   bool
	calls  int
}

func newMuseQuotaFixture(t *testing.T) *museQuotaFixture {
	t.Helper()
	dir := storetest.DataDir(t)
	if err := MigrateStores(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := &accountstore.Store{Root: filepath.Join(dir, "accounts")}
	var account accountstore.Metadata
	if err := registry.Update(func(st *accountstore.State) error {
		var err error
		account, err = st.Save("muse", "", "quota-identity", "quota", accountstore.Credential{Schema: "fixture", Payload: json.RawMessage(`{"token":"credential-canary"}`)})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_turns(session,n,revision,turn) VALUES ('ses_quota',1,1,'{"n":1,"status":"ok"}');
INSERT INTO runs(id,session,started,body) VALUES ('wf_quota','',0,'{"id":"wf_quota","status":"ok"}');`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 21, 4, 0, 0, 0, time.UTC)
	ref := callspec.ExecutionRef{Session: "ses_quota", Turn: 1, AdmissionID: "ses_quota/1/native/1"}
	runRef := callspec.ExecutionRef{Run: "wf_quota", Agent: 2, AdmissionID: "wf_quota/1/native/1"}
	if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		for _, execution := range []callspec.ExecutionRef{ref, runRef} {
			_, err := attempts.SaveTx(t.Context(), conn, attempts.Fact{ExecutionRef: execution, AccountID: account.ID, Identity: account.Identity, Profile: "muse", Harness: "muse", WorkDir: "/repo", AdmittedAt: now.Add(-time.Minute)})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f := &museQuotaFixture{t: t, db: db, store: registry, runRef: runRef,
		input:  callspec.MuseQuotaObservation{ExecutionRef: ref, Identity: account.Identity, ObservedAt: now, ResetsAt: now.Add(time.Hour)},
		caller: websvc.Caller{Kind: websvc.Session, Session: ref.Session},
	}
	s := services{accounts: accountservice.New(accountservice.Options{Store: registry})}
	calls, err := websvc.Bind([]websvc.Binding{s.museQuotaBinding()})
	if err != nil {
		t.Fatal(err)
	}
	observe := calls["ObserveMuseQuota"]
	calls["ObserveMuseQuota"] = func(ctx context.Context, args []json.RawMessage) (any, error) {
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()
		result, err := observe(ctx, args)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.drop && err == nil {
			f.drop = false
			return nil, errors.New("reply lost after observation committed")
		}
		return result, err
	}
	applyOnce(calls, state.For(db), t.Logf)
	enginetest.ServeCallers(t, dir, func(net.Conn) (websvc.Caller, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.caller, nil
	}, calls)
	f.client = engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	return f
}

func (f *museQuotaFixture) pose(c websvc.Caller) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caller = c
}

func (f *museQuotaFixture) send(id string, in callspec.MuseQuotaObservation) error {
	return f.client.Call(callspec.WithRequest(f.t.Context(), id), "ObserveMuseQuota", []any{in}, nil)
}

type quotaSecretFile struct {
	Content  string
	Mode     os.FileMode
	Modified time.Time
}

func quotaSecretSnapshot(t *testing.T, root string) map[string]quotaSecretFile {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "credentials", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]quotaSecretFile{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		out[path] = quotaSecretFile{Content: string(raw), Mode: info.Mode(), Modified: info.ModTime()}
	}
	return out
}

func TestMuseQuotaBindingReplaysMetadataWithoutCredentialWrites(t *testing.T) {
	f := newMuseQuotaFixture(t)
	before := quotaSecretSnapshot(t, f.store.Root)
	f.mu.Lock()
	f.drop = true
	f.mu.Unlock()
	id := "ses_quota/1/quota/1"
	if err := f.send(id, f.input); err == nil || !strings.Contains(err.Error(), "reply lost after observation committed") {
		t.Fatalf("commit before reply = %v", err)
	}
	if err := f.send(id, f.input); err != nil {
		t.Fatalf("unchanged replay = %v", err)
	}
	changed := f.input
	changed.ResetsAt = changed.ResetsAt.Add(time.Hour)
	if err := f.send(id, changed); err == nil || !strings.Contains(err.Error(), "request ID was reused") {
		t.Fatalf("changed observation returned cached success: %v", err)
	}
	f.mu.Lock()
	handlers := f.calls
	f.mu.Unlock()
	if handlers != 1 {
		t.Fatalf("metadata handler ran %d times", handlers)
	}
	if !reflect.DeepEqual(before, quotaSecretSnapshot(t, f.store.Root)) {
		t.Fatal("observation replay or conflict changed credential files")
	}
	st, err := f.store.Read()
	if err != nil || len(st.Entries) != 1 || st.Entries[0].Observation == nil || !st.Entries[0].Observation.RateLimitedUntil.Equal(f.input.ResetsAt) {
		t.Fatal("observation was lost or changed by replay", err)
	}
}

func TestMuseQuotaBindingChecksCallerBeforeSavedReply(t *testing.T) {
	f := newMuseQuotaFixture(t)
	before := quotaSecretSnapshot(t, f.store.Root)
	for _, tc := range []struct {
		name   string
		caller websvc.Caller
	}{
		{"owner", websvc.Caller{Kind: websvc.Owner}},
		{"unverified", websvc.Caller{Kind: websvc.Unverified, Session: "ses_quota"}},
		{"other-session", websvc.Caller{Kind: websvc.Session, Session: "ses_other"}},
		{"worker", websvc.Caller{Kind: websvc.Worker, Run: "wf_quota", Agent: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.pose(tc.caller)
			id := "ses_quota/1/quota/" + tc.name
			// Even an existing caller-scoped receipt cannot bypass execution checks.
			key := callerRequestKey(tc.caller, true, websvc.Principal{Kind: "local"}, id)
			if err := state.For(f.db).FinishRequest(t.Context(), key, "ObserveMuseQuota", []byte("null")); err != nil {
				t.Fatal(err)
			}
			if err := f.send(id, f.input); err == nil {
				t.Fatal("unverified execution received cached success")
			}
		})
	}
	if !reflect.DeepEqual(before, quotaSecretSnapshot(t, f.store.Root)) {
		t.Fatal("refused execution changed credentials")
	}
}

func TestMuseQuotaBindingAllowsTerminalExecutionButRequiresAdmission(t *testing.T) {
	f := newMuseQuotaFixture(t)
	if err := f.send("ses_quota/1/quota/valid", f.input); err != nil {
		t.Fatalf("terminal session observation = %v", err)
	}
	run := f.input
	run.ExecutionRef = f.runRef
	f.pose(websvc.Caller{Kind: websvc.Worker, Run: f.runRef.Run, Agent: 0})
	if err := f.send("wf_quota/1/quota/valid", run); err != nil {
		t.Fatalf("terminal coordinator observation = %v", err)
	}
	for _, tc := range []struct {
		name string
		in   callspec.MuseQuotaObservation
	}{
		{"no-admission", callspec.MuseQuotaObservation{ExecutionRef: callspec.ExecutionRef{Run: f.runRef.Run, Agent: 2}, Identity: run.Identity, ObservedAt: run.ObservedAt, ResetsAt: run.ResetsAt}},
		{"wrong-parent", callspec.MuseQuotaObservation{ExecutionRef: callspec.ExecutionRef{Run: f.runRef.Run, Agent: 3, AdmissionID: f.runRef.AdmissionID}, Identity: run.Identity, ObservedAt: run.ObservedAt, ResetsAt: run.ResetsAt}},
	} {
		if err := f.send("wf_quota/1/quota/"+tc.name, tc.in); err == nil {
			t.Fatalf("%s succeeded", tc.name)
		}
	}
	if err := f.send("ses_other/1/quota/foreign-prefix", run); err == nil {
		t.Fatal("foreign request prefix succeeded")
	}
	if err := f.send("", run); err == nil {
		t.Fatal("request without an ID succeeded")
	}
}
