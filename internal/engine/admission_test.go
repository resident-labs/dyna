package engine

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/account/attempts"
	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/database"
	"dyna-agent/internal/engineclient"
	"dyna-agent/internal/enginetest"
	harnessruntime "dyna-agent/internal/harness/runtime"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/profile"
	"dyna-agent/internal/roomsvc"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"dyna-agent/internal/websvc"
)

type admissionFixture struct {
	t         *testing.T
	s         services
	dir       string
	db        *sql.DB
	meta      subagent.Meta
	input     callspec.AccountAdmission
	client    *engineclient.Client
	mu        sync.RWMutex
	caller    websvc.Caller
	dropReply bool
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	dir := storetest.DataDir(t)
	if err := MigrateStores(dir); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	accounts := &harnessruntime.Accounts{Root: filepath.Join(dir, "accounts")}
	n, err := accounts.Create("codex", "admission")
	if err != nil {
		t.Fatal(err)
	}
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"admission","chatgpt_account_id":"workspace"}}`))
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"id_token": "header." + claims + ".signature", "access_token": "test-access", "refresh_token": "test-refresh", "account_id": "workspace"}})
	if err := os.WriteFile(filepath.Join(n.Home, "auth.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	n, err = accounts.Capture(t.Context(), n.ID, "/fake/codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&accountstore.Store{Root: accounts.Root}).SetActive(accountstore.ProviderChatGPT, "codex", "", n.ID); err != nil {
		t.Fatal(err)
	}
	profiles := &profile.Store{Profiles: []profile.Profile{
		{Name: "worker", Harness: profile.HarnessCodex},
		{Name: "disabled", Harness: profile.HarnessCodex, Disabled: true},
		{Name: "session-only", Harness: profile.HarnessCodex, Uses: []string{profile.UseAgents}},
	}}
	m := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(dir, "sessions")}, Profiles: func() (*profile.Store, error) { return profiles, nil }}
	meta := subagent.Meta{ID: "ses_admission", Profile: "worker", Dir: t.TempDir(), Turns: 1, Status: subagent.StatusIdle}
	if err := sessionlife.Save(m.Store.Root, meta.ID, meta); err != nil {
		t.Fatal(err)
	}
	turn, _ := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnRunning})
	if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn) VALUES(?,?,?,?)", meta.ID, 1, 1, turn); err != nil {
		t.Fatal(err)
	}
	rooms := roomsvc.New(nil)
	rooms.Notify.Sessions = m
	f := &admissionFixture{t: t, dir: dir, db: db, meta: meta, s: services{rooms: rooms, runtime: &harnessruntime.Runtime{Accounts: accounts}},
		input:  callspec.AccountAdmission{ExecutionRef: callspec.ExecutionRef{Session: meta.ID, Turn: 1}, Profile: "worker", Harness: "codex", AccountID: n.ID, WorkDir: meta.Dir},
		caller: websvc.Caller{Kind: websvc.Session, Session: meta.ID},
	}
	calls, err := websvc.Bind([]websvc.Binding{f.s.admissionBinding(), {
		Name: "CheckHistoricalExecution", Access: websvc.Read, Scope: websvc.ScopeOperate, Local: true,
		Method: checkExecutionCaller,
	}})
	if err != nil {
		t.Fatal(err)
	}
	admit := calls["AdmitNativeAttempt"]
	calls["AdmitNativeAttempt"] = func(ctx context.Context, args []json.RawMessage) (any, error) {
		result, err := admit(ctx, args)
		f.mu.Lock()
		drop := f.dropReply && err == nil
		if drop {
			f.dropReply = false
		}
		f.mu.Unlock()
		if drop {
			return nil, errors.New("response failed after admission committed")
		}
		return result, err
	}
	applyOnce(calls, state.For(db), t.Logf)
	enginetest.ServeCallers(t, dir, func(net.Conn) (websvc.Caller, error) { f.mu.RLock(); defer f.mu.RUnlock(); return f.caller, nil }, calls)
	f.client = engineclient.New("", engineclient.RestartWait(0), engineclient.Notices(nil))
	return f
}

func (f *admissionFixture) pose(c websvc.Caller) { f.mu.Lock(); defer f.mu.Unlock(); f.caller = c }
func (f *admissionFixture) call(id string, in callspec.AccountAdmission) (callspec.ExecutionRef, error) {
	var ref callspec.ExecutionRef
	err := f.client.Call(callspec.WithRequest(f.t.Context(), id), "AdmitNativeAttempt", []any{in}, &ref)
	return ref, err
}

func TestNativeAdmissionUsesVerifiedExecutionAndImmutableFacts(t *testing.T) {
	f := newAdmissionFixture(t)
	id := f.meta.ID + "/1/process/1"
	ref, err := f.call(id, f.input)
	if err != nil || ref.AdmissionID != id || ref.Session != f.meta.ID || ref.Turn != 1 {
		t.Fatalf("admission: %+v %v", ref, err)
	}
	fact, err := attempts.Load(t.Context(), f.dir, ref)
	if err != nil || fact.Identity == "" || fact.Profile != "worker" || fact.AccountID != f.input.AccountID || fact.Repo != "" || fact.WorkDir != f.meta.Dir || fact.AdmittedAt.IsZero() {
		t.Fatalf("stored facts: %+v %v", fact, err)
	}
	again, err := f.call(id, f.input)
	if err != nil || again != ref {
		t.Fatalf("same request: %+v %v", again, err)
	}
	for _, change := range []func(*callspec.AccountAdmission){
		func(in *callspec.AccountAdmission) { in.WorkDir = t.TempDir() },
		func(in *callspec.AccountAdmission) { in.AccountID = "another" },
		func(in *callspec.AccountAdmission) { in.Profile = "disabled" },
		func(in *callspec.AccountAdmission) { in.Harness = "muse" },
		func(in *callspec.AccountAdmission) { in.Turn = 2 },
	} {
		in := f.input
		change(&in)
		if _, err := f.call(id, in); err == nil || !strings.Contains(err.Error(), "request ID was reused") {
			t.Fatalf("changed request returned cached approval: %+v %v", in, err)
		}
	}
	for _, caller := range []websvc.Caller{
		{Kind: websvc.Owner}, {Kind: websvc.Unverified, Session: f.meta.ID}, {Kind: websvc.Session, Session: "ses_other"},
		{Kind: websvc.Worker, Run: "wf_other"},
	} {
		f.pose(caller)
		if got, err := f.call(id, f.input); err == nil || got.AdmissionID != "" {
			t.Fatalf("caller %+v read cached approval %+v: %v", caller, got, err)
		}
	}
	f.pose(websvc.Caller{Kind: websvc.Session, Session: f.meta.ID})
	if _, err := f.call("", f.input); err == nil {
		t.Fatal("admission without a request ID passed")
	}
	if _, err := f.call("ses_other/stolen", f.input); err == nil {
		t.Fatal("foreign request prefix passed")
	}
	if _, err := f.s.admitNativeAttempt(callspec.WithRequest(t.Context(), id), f.input); err == nil {
		t.Fatal("context without a verified socket caller passed")
	}
}

func TestNativeAdmissionChecksLiveTurnAndSavedDirectory(t *testing.T) {
	f := newAdmissionFixture(t)
	wrong := f.input
	wrong.WorkDir = t.TempDir()
	if _, err := f.call(f.meta.ID+"/wrong-dir", wrong); err == nil {
		t.Fatal("session supplied another directory")
	}
	wrong = f.input
	wrong.Profile = "session-only"
	if _, err := f.call(f.meta.ID+"/wrong-profile", wrong); err == nil {
		t.Fatal("session supplied another profile")
	}
	turn, _ := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnRunning, StopAsked: true})
	if err := state.For(f.db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(t.Context(), "UPDATE session_turns SET turn=? WHERE session=? AND n=1", turn, f.meta.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(f.meta.ID+"/stopped", f.input); err == nil {
		t.Fatal("a stopped turn admitted a new process")
	}
}

func TestNativeAdmissionReceiptContainsReplyBeforeDispatcherFinish(t *testing.T) {
	f := newAdmissionFixture(t)
	f.mu.Lock()
	f.dropReply = true
	f.mu.Unlock()
	id := f.meta.ID + "/commit-before-reply"
	if _, err := f.call(id, f.input); err == nil {
		t.Fatal("fixture did not lose the response")
	}
	want := f.input.ExecutionRef
	want.AdmissionID = id
	raw, done, err := state.For(f.db).RequestDone(t.Context(), runnerRequestKey(id))
	var committed callspec.ExecutionRef
	decodeErr := json.Unmarshal(raw, &committed)
	if err != nil || !done || decodeErr != nil || committed != want {
		t.Fatalf("atomic acknowledgement: %s %v %v %v", raw, done, err, decodeErr)
	}
	got, err := f.call(id, f.input)
	if err != nil || got != want {
		t.Fatalf("replay after response failure: %+v %v", got, err)
	}
	if _, err := attempts.Load(t.Context(), f.dir, got); err != nil {
		t.Fatal(err)
	}
}

func TestNativeAdmissionWorkflowCoordinatorAndProfilePolicy(t *testing.T) {
	f := newAdmissionFixture(t)
	const runID = "wf_admission"
	run := runstore.Meta{ID: runID, Status: "running", StartedAt: time.Now()}
	body, _ := json.Marshal(run)
	if _, err := f.db.Exec("INSERT INTO runs(id,session,started,body) VALUES(?,?,?,?)", runID, "", run.StartedAt.UnixNano(), string(body)); err != nil {
		t.Fatal(err)
	}
	in := f.input
	in.ExecutionRef = callspec.ExecutionRef{Run: runID, Agent: 2}
	// Workflow cwd is an operation input and may lie outside a session's cwd.
	in.WorkDir = t.TempDir()
	f.pose(websvc.Caller{Kind: websvc.Worker, Run: runID})
	ref, err := f.call(runID+"/coordinator/1", in)
	if err != nil || ref.Run != runID || ref.Agent != 2 || ref.AdmissionID == "" {
		t.Fatalf("coordinator admission: %+v %v", ref, err)
	}
	for _, caller := range []websvc.Caller{
		{Kind: websvc.Worker, Run: runID, Agent: 2},
		{Kind: websvc.Worker, Run: runID, Agent: 1},
		{Kind: websvc.Worker, Run: "wf_other"},
		{Kind: websvc.Unverified, Run: runID},
		{Kind: websvc.Owner},
	} {
		f.pose(caller)
		if _, err := f.call(ref.AdmissionID, in); err == nil {
			t.Fatalf("caller %+v read coordinator acknowledgement", caller)
		}
	}
	f.pose(websvc.Caller{Kind: websvc.Worker, Run: runID})
	for _, name := range []string{"disabled", "session-only", "missing"} {
		bad := in
		bad.Profile = name
		if _, err := f.call(runID+"/profile/"+name, bad); err == nil {
			t.Fatalf("workflow admitted profile %s", name)
		}
	}
	run.Status = "ok"
	run.EndedAt = time.Now()
	body, _ = json.Marshal(run)
	if _, err := f.db.Exec("UPDATE runs SET body=? WHERE id=?", string(body), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(runID+"/terminal", in); err == nil {
		t.Fatal("terminal run admitted another process")
	}
}

func TestNativeAdmissionRechecksExecutionBeforeCommitting(t *testing.T) {
	f := newAdmissionFixture(t)
	account, err := f.s.runtime.Accounts.CheckPreparedAccount("codex", f.input.AccountID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := readAdmissionOwner(t.Context(), f.db, f.input.ExecutionRef)
	if err != nil {
		t.Fatal(err)
	}
	w := state.For(f.db)
	for _, change := range []string{"directory", "profile", "closed", "turn"} {
		t.Run(change, func(t *testing.T) {
			meta := f.meta
			turn := subagent.Turn{N: 1, Status: subagent.TurnRunning}
			switch change {
			case "directory":
				meta.Dir = t.TempDir()
			case "profile":
				meta.Profile = "session-only"
			case "closed":
				meta.Closed = time.Now()
			case "turn":
				turn.StopAsked = true
			}
			if err := sessionlife.Save(f.s.rooms.Notify.Sessions.Store.Root, meta.ID, meta); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(turn)
			if _, err := f.db.Exec("UPDATE session_turns SET turn=? WHERE session=? AND n=1", raw, meta.ID); err != nil {
				t.Fatal(err)
			}
			ref := f.input.ExecutionRef
			ref.AdmissionID = f.meta.ID + "/changed/" + change
			fact := attempts.Fact{ExecutionRef: ref, AccountID: account.ID, Identity: account.Identity, Profile: "worker", Harness: "codex", WorkDir: f.meta.Dir, AdmittedAt: time.Now()}
			ctx := state.ForRequest(t.Context(), runnerRequestKey(ref.AdmissionID))
			if err := f.s.writeNativeAdmission(ctx, w, owner, account, fact); err == nil {
				t.Fatal("stale execution committed an admission")
			}
			if _, err := attempts.Load(t.Context(), f.dir, ref); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("failed admission left a fact: %v", err)
			}
			if _, done, err := w.RequestDone(t.Context(), runnerRequestKey(ref.AdmissionID)); err != nil || done {
				t.Fatalf("failed admission left a receipt: %v %v", done, err)
			}
		})
	}
}

func TestHistoricalExecutionCallerDoesNotRequireLiveState(t *testing.T) {
	f := newAdmissionFixture(t)
	ref, err := f.call(f.meta.ID+"/admit", f.input)
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := json.Marshal(subagent.Turn{N: 1, Status: subagent.TurnOK})
	if _, err := f.db.Exec("UPDATE session_turns SET turn=? WHERE session=? AND n=1", turn, f.meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := (&accountstore.Store{Root: f.s.runtime.Accounts.Root}).SetActive(accountstore.ProviderChatGPT, "codex", "", ""); err != nil {
		t.Fatal(err)
	}
	ctx := callspec.WithRequest(t.Context(), f.meta.ID+"/historical-observation")
	if err := f.client.Call(ctx, "CheckHistoricalExecution", []any{ref}, nil); err != nil {
		t.Fatalf("own historical execution caller: %v", err)
	}
	if _, err := attempts.Load(t.Context(), f.dir, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(f.meta.ID+"/fresh-attempt", f.input); err == nil {
		t.Fatal("historical caller check bypassed live admission")
	}
}
