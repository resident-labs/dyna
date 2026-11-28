package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

const (
	tokOld = "TOKEN-OLD-SENTINEL-11aa"
	tokNew = "TOKEN-NEW-SENTINEL-22bb"
)

// fakeAgyHosts is the hosts' side: the engine host's token is the real main token file, the others' are in memory. It records what the
// engine asked and fails where a test says. It never runs agy or ssh.
type fakeAgyHosts struct {
	mu       sync.Mutex
	rig      *agyRig
	names    []contract.HostName
	tokens   map[contract.HostName]string
	down     map[contract.HostName]error // AgyHostAnswers
	failOn   map[contract.HostName]error // AgyInstall
	installs []contract.HostName         // the installs that changed a token; a host that had it already is not one
	stall    map[contract.HostName]bool  // AgyInstall waits for its context
	takeback map[contract.HostName]string
}

func (f *fakeAgyHosts) AgyHostNames(context.Context) ([]contract.HostName, error) {
	return f.names, nil
}
func (f *fakeAgyHosts) AgyHostAnswers(_ context.Context, h contract.HostName) error {
	return f.down[h]
}
func (f *fakeAgyHosts) read(h contract.HostName) string {
	if h == AgySource {
		b, _ := os.ReadFile(f.rig.layout.HostAgyMainToken())
		return string(b)
	}
	return f.tokens[h]
}
func (f *fakeAgyHosts) AgyInstall(ctx context.Context, h contract.HostName, stored, takeback string) (int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := os.ReadFile(stored)
	// What the host's script decides under its lock: a token that already is the incoming one is left alone, with no take-back.
	if string(b) == f.read(h) {
		return 0, true, nil
	}
	f.installs = append(f.installs, h)
	f.takeback[h] = takeback
	if f.stall[h] {
		f.mu.Unlock()
		<-ctx.Done()
		f.mu.Lock()
		return 0, false, ctx.Err()
	}
	if err := f.failOn[h]; err != nil {
		return 0, false, err
	}
	if takeback != "" {
		if err := os.WriteFile(takeback, []byte(f.read(h)), 0o600); err != nil {
			return 0, false, err
		}
	}
	if h == AgySource {
		os.WriteFile(f.rig.layout.HostAgyMainToken(), b, 0o600)
	} else {
		f.tokens[h] = string(b)
	}
	return 2, false, nil
}

// useRig has two added accounts: old (active, its token in main) and new (stored only), and a second host.
type useRig struct {
	*agyRig
	hosts    *fakeAgyHosts
	old, new contract.AccountID
}

func newUseRig(t *testing.T) *useRig {
	t.Helper()
	r := &useRig{agyRig: newAgyRig(t)}
	r.hosts = &fakeAgyHosts{rig: r.agyRig, names: []contract.HostName{AgySource, "node"}, tokens: map[contract.HostName]string{"node": tokOld},
		down: map[contract.HostName]error{}, failOn: map[contract.HostName]error{}, stall: map[contract.HostName]bool{}, takeback: map[contract.HostName]string{}}
	r.actions.SetAgyHosts(r.hosts)
	r.actions.agyTurnWait, r.actions.agyTurnPoll = 200*time.Millisecond, 20*time.Millisecond
	add := func(label, token string) contract.AccountID {
		id, _ := store.NewAgyAccountID()
		if err := r.st.AddAgyAccount(r.ctx, id, label, time.Now()); err != nil {
			t.Fatal(err)
		}
		writeFile(t, r.layout.HostAgyToken(string(id)), token)
		return id
	}
	r.old, r.new = add("Old", tokOld), add("New", tokNew)
	writeFile(t, r.layout.HostAgyMainToken(), tokOld)
	if err := r.st.ActivateAgyAccount(r.ctx, r.old); err != nil {
		t.Fatal(err)
	}
	return r
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *useRig) use(name string) (contract.AgyUseResponse, *contract.Error) {
	return r.actions.AgyUse(r.ctx, owner, contract.AgyUseRequest{Account: name})
}

func (r *useRig) state() store.AgyUse {
	u, err := r.st.AgyUse(r.ctx)
	if err != nil {
		panic(err)
	}
	return u
}

func file(path string) string { b, _ := os.ReadFile(path); return string(b) }

var owner = ownerCaller

func sqlExec(t *testing.T, st *store.Store, query string, args ...any) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *sql.Tx) error { _, err := tx.Exec(query, args...); return err })
	if err != nil {
		t.Fatal(err)
	}
}

func rows(t *testing.T, st *store.Store, query string, args ...any) (n int) {
	t.Helper()
	if err := st.Read().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestUseRefusesBeforeChangingAnything(t *testing.T) {
	r := newUseRig(t)
	if _, problem := r.actions.AgyUse(r.ctx, contract.Principal{Kind: contract.PrincipalSession}, contract.AgyUseRequest{Account: "New"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a session switched: %v", problem)
	}
	if _, problem := r.use("Nobody"); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("unknown account: %v", problem)
	}
	// A host that does not answer: nothing changed, held not set.
	r.hosts.down["node"] = errors.New("ssh timed out")
	if _, problem := r.use("New"); problem == nil || problem.Code != contract.ErrUnavailable || !strings.Contains(problem.Message, "node") {
		t.Fatalf("a host down: %v", problem)
	}
	r.hosts.down = map[contract.HostName]error{}
	// A seed, switch or import running.
	end, ok := r.actions.service.AgyExclusive()
	if !ok {
		t.Fatal("exclusive refused")
	}
	if _, problem := r.use("New"); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("while another runs: %v", problem)
	}
	end()
	// The active account is not switched to again; a token that is missing is refused.
	if _, problem := r.use("Old"); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("active again: %v", problem)
	}
	os.Remove(r.layout.HostAgyToken(string(r.new)))
	if _, problem := r.use("New"); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("no stored sign-in: %v", problem)
	}
	if u := r.state(); u.Held || u.Active != r.old || len(r.hosts.installs) != 0 || file(r.layout.HostAgyMainToken()) != tokOld || r.hosts.tokens["node"] != tokOld {
		t.Fatalf("something changed: %+v installs %v", u, r.hosts.installs)
	}
}

func TestUseRefusesWhenNoAccountIsActive(t *testing.T) {
	r := newUseRig(t)
	sqlExec(t, r.st, "DELETE FROM agy_account")
	if _, problem := r.use("New"); problem == nil || problem.Code != contract.ErrInvalid || !strings.Contains(problem.Message, "import") {
		t.Fatalf("no active account: %v", problem)
	}
}

func TestUseSwapsEveryHostTakesBackAndActivatesInOneStep(t *testing.T) {
	r := newUseRig(t)
	// The main token is the live one: agy refreshed it, so the take-back must carry THAT, not the old stored copy.
	writeFile(t, r.layout.HostAgyMainToken(), tokOld+"-REFRESHED")
	out, problem := r.use("New")
	if problem != nil {
		t.Fatal(problem)
	}
	if len(out.Hosts) != 2 || out.Hosts[0].Host != AgySource || out.Hosts[1].Host != "node" || !strings.HasPrefix(out.Hosts[0].Result, "ok") || !strings.Contains(out.Hosts[0].Result, "ended 2") {
		t.Fatalf("hosts: %+v", out.Hosts)
	}
	if file(r.layout.HostAgyMainToken()) != tokNew || r.hosts.tokens["node"] != tokNew {
		t.Fatalf("tokens: main %q node %q", file(r.layout.HostAgyMainToken()), r.hosts.tokens["node"])
	}
	if got := file(r.layout.HostAgyToken(string(r.old))); got != tokOld+"-REFRESHED" {
		t.Fatalf("the take-back is %q", got)
	}
	// The take-back is asked of the engine host only, and goes to the account that held the token.
	if r.hosts.takeback[AgySource] != r.layout.HostAgyToken(string(r.old)) || r.hosts.takeback["node"] != "" {
		t.Fatalf("take-backs: %v", r.hosts.takeback)
	}
	if u := r.state(); u.Active != r.new || u.Held || u.StuckHost != "" || u.Target != "" {
		t.Fatalf("state: %+v", u)
	}
	if got := r.hosts.installs; len(got) != 2 || got[0] != AgySource {
		t.Fatalf("order: %v", got)
	}
	// Cancel is a use of the old account: it swaps back, and the new account's copy takes the new token back.
	if _, problem := r.use("Old"); problem != nil {
		t.Fatal(problem)
	}
	if file(r.layout.HostAgyMainToken()) != tokOld+"-REFRESHED" || r.state().Active != r.old {
		t.Fatalf("back: %q %+v", file(r.layout.HostAgyMainToken()), r.state())
	}
}

func TestUseWithAHostFailingStaysHeldNamesItAndARerunSkipsHostsDone(t *testing.T) {
	r := newUseRig(t)
	r.hosts.failOn["node"] = errors.New("could not swap the token file on the host")
	out, problem := r.use("New")
	if problem == nil || problem.Code != contract.ErrUnavailable || !strings.Contains(problem.Message, "host node") || len(out.Hosts) != 1 {
		t.Fatalf("a failing host: %v %+v", problem, out)
	}
	u := r.state()
	if !u.Held || u.StuckHost != "node" || u.Target != r.new || u.Active != r.old {
		t.Fatalf("state: %+v", u)
	}
	// The sessions stay held: the account the engine host holds is not the active one.
	if held, _ := r.actions.service.AgySwitchHeld(r.ctx); !held {
		t.Fatal("not held")
	}
	// The failed host is back; the re-run skips the engine host, whose token compares equal, and the take-back is not repeated.
	r.hosts.failOn = map[contract.HostName]error{}
	r.hosts.installs, r.hosts.takeback = nil, map[contract.HostName]string{}
	out, problem = r.use("New")
	if problem != nil {
		t.Fatal(problem)
	}
	if len(r.hosts.installs) != 1 || r.hosts.installs[0] != "node" || !strings.Contains(out.Hosts[0].Result, "already") {
		t.Fatalf("the re-run: installs %v hosts %+v", r.hosts.installs, out.Hosts)
	}
	if file(r.layout.HostAgyToken(string(r.old))) != tokOld {
		t.Fatalf("the re-run overwrote the old account's copy: %q", file(r.layout.HostAgyToken(string(r.old))))
	}
	if u := r.state(); u.Held || u.Active != r.new {
		t.Fatalf("after the re-run: %+v", u)
	}
}

// A switch whose hermeslab swap finished but whose result was lost (the engine went down, or the caller left) is held with its target.
// Only a run for that target is accepted: it finds hermeslab done, takes nothing back, and finishes; the old account is refused with the
// text, and the old account's stored copy is never overwritten with the new token.
func TestAHeldSwitchAcceptsOnlyItsTargetAndARerunFinishesWithoutATakeBack(t *testing.T) {
	r := newUseRig(t)
	// The state a lost result leaves: held for the new account, hermeslab already holding the new token, node not swapped.
	if _, err := r.st.BeginAgySwitch(r.ctx, r.new); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.layout.HostAgyMainToken(), tokNew)
	_, problem := r.use("Old")
	if problem == nil || problem.Code != contract.ErrConflict || !strings.Contains(problem.Message, "finish the switch to New first") {
		t.Fatalf("the old account while held: %v", problem)
	}
	if len(r.hosts.installs) != 0 || file(r.layout.HostAgyToken(string(r.old))) != tokOld || !r.state().Held {
		t.Fatalf("a refused use changed something: installs %v old %q %+v", r.hosts.installs, file(r.layout.HostAgyToken(string(r.old))), r.state())
	}
	out, problem := r.use("New")
	if problem != nil {
		t.Fatal(problem)
	}
	if !strings.Contains(out.Hosts[0].Result, "already") || len(r.hosts.installs) != 1 || r.hosts.installs[0] != "node" {
		t.Fatalf("the rerun: %+v installs %v", out.Hosts, r.hosts.installs)
	}
	if file(r.layout.HostAgyToken(string(r.old))) != tokOld || file(r.layout.HostAgyMainToken()) != tokNew || r.hosts.tokens["node"] != tokNew {
		t.Fatalf("copies: old %q main %q node %q", file(r.layout.HostAgyToken(string(r.old))), file(r.layout.HostAgyMainToken()), r.hosts.tokens["node"])
	}
	if u := r.state(); u.Held || u.Active != r.new || u.Target != "" {
		t.Fatalf("after the rerun: %+v", u)
	}
	// The way back is a new switch, after the first one is done.
	if _, problem := r.use("Old"); problem != nil {
		t.Fatal(problem)
	}
}

// A refreshed outgoing token (one that matches no stored copy) is taken back on the rerun, to the outgoing account: it is the active one
// until every host is done.
func TestARerunTakesBackAnOutgoingTokenThatDiffersFromTheTarget(t *testing.T) {
	r := newUseRig(t)
	r.hosts.failOn["node"] = errors.New("x")
	writeFile(t, r.layout.HostAgyMainToken(), tokOld+"-REFRESHED")
	r.use("New")                                               // hermeslab swapped (and took the refreshed token back); node failed
	writeFile(t, r.layout.HostAgyMainToken(), tokOld+"-AGAIN") // something put another outgoing token there before the rerun
	r.hosts.failOn = map[contract.HostName]error{}
	if _, problem := r.use("New"); problem != nil {
		t.Fatal(problem)
	}
	if file(r.layout.HostAgyToken(string(r.old))) != tokOld+"-AGAIN" || file(r.layout.HostAgyMainToken()) != tokNew {
		t.Fatalf("old %q main %q", file(r.layout.HostAgyToken(string(r.old))), file(r.layout.HostAgyMainToken()))
	}
}

// An engine restart keeps the held switch: the target and the refusal come from the stored row.
func TestAHeldSwitchSurvivesAnEngineRestart(t *testing.T) {
	r := newUseRig(t)
	r.hosts.failOn["node"] = errors.New("x")
	r.use("New")
	again := NewAccountActions(r.actions.service, r.actions.connections, r.actions.quotas)
	again.SetAgyHosts(r.hosts)
	if _, problem := again.AgyUse(r.ctx, owner, contract.AgyUseRequest{Account: "Old"}); problem == nil || !strings.Contains(problem.Message, "finish the switch to New first") {
		t.Fatalf("after a restart: %v", problem)
	}
	if u := r.state(); !u.Held || u.Target != r.new || u.StuckHost != "node" {
		t.Fatalf("state: %+v", u)
	}
	r.hosts.failOn = map[contract.HostName]error{}
	if _, problem := again.AgyUse(r.ctx, owner, contract.AgyUseRequest{Account: "New"}); problem != nil {
		t.Fatal(problem)
	}
}

// A host that stalls fails within its bound, names itself, and the switch stays held.
func TestAStalledHostFailsWithinItsBoundAndStaysHeld(t *testing.T) {
	r := newUseRig(t)
	r.actions.agyHostWait = 100 * time.Millisecond
	r.hosts.stall["node"] = true
	start := time.Now()
	_, problem := r.use("New")
	if problem == nil || !strings.Contains(problem.Message, "host node") || !strings.Contains(problem.Message, "did not finish") {
		t.Fatalf("a stalled host: %v", problem)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the stall was not bounded")
	}
	if u := r.state(); !u.Held || u.StuckHost != "node" || u.Active != r.old {
		t.Fatalf("state: %+v", u)
	}
}

// Remove and a switch's target. Remove wins when it runs between the switch's checks and its lock: the switch fails before anything is
// held. The hook blocks the switch there, and Remove runs meanwhile.
func TestRemovingTheTargetBetweenTheChecksAndTheHoldWins(t *testing.T) {
	r := newUseRig(t)
	reached, proceed := make(chan struct{}), make(chan struct{})
	r.actions.afterAgyPreflight = func() { close(reached); <-proceed }
	done := make(chan *contract.Error, 1)
	go func() { _, problem := r.use("New"); done <- problem }()
	<-reached
	if _, problem := r.actions.AccountsRemove(r.ctx, owner, contract.AccountsRemoveRequest{AccountID: r.new}); problem != nil {
		t.Fatalf("remove: %v", problem)
	}
	close(proceed)
	if problem := <-done; problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("the switch after the account was removed: %v", problem)
	}
	if u := r.state(); u.Held || u.Target != "" || len(r.hosts.installs) != 0 {
		t.Fatalf("something was held or swapped: %+v %v", u, r.hosts.installs)
	}
}

// The hold wins when it is set first: Remove of the target is refused while the switch waits for running turns, and works after it.
func TestRemovingTheTargetOfAHeldSwitchIsRefused(t *testing.T) {
	r := newUseRig(t)
	r.actions.agyTurnWait = 3 * time.Second
	sqlExec(t, r.st, `INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('a','r','a','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,harness) VALUES('ba','a','','agent','ready','','',0,0,'agy');
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t1','a',1,'running','m',0,0,0,0)`)
	done := make(chan *contract.Error, 1)
	go func() { _, problem := r.use("New"); done <- problem }()
	for !r.state().Held {
		time.Sleep(5 * time.Millisecond)
	}
	if _, problem := r.actions.AccountsRemove(r.ctx, owner, contract.AccountsRemoveRequest{AccountID: r.new}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("the target was removed while held: %v", problem)
	}
	sqlExec(t, r.st, "UPDATE turns SET ended_at=2,outcome='completed'")
	if problem := <-done; problem != nil {
		t.Fatal(problem)
	}
}

// A run that releases because turns are still running releases only a hold it set itself: one an earlier run left stays.
func TestTurnsStillRunningKeepAnEarlierRunsHold(t *testing.T) {
	r := newUseRig(t)
	if _, err := r.st.BeginAgySwitch(r.ctx, r.new); err != nil {
		t.Fatal(err)
	}
	sqlExec(t, r.st, `INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('a','r','a','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,harness) VALUES('ba','a','','agent','ready','','',0,0,'agy');
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t1','a',1,'running','m',0,0,0,0)`)
	if _, problem := r.use("New"); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("running turns: %v", problem)
	}
	if u := r.state(); !u.Held || u.Target != r.new {
		t.Fatalf("the earlier hold was released: %+v", u)
	}
}

// Turns still running after the wait release the hold and fail with their count, and nothing was swapped.
func TestUseWithTurnsStillRunningReleasesAndFailsWithTheCount(t *testing.T) {
	r := newUseRig(t)
	sqlExec(t, r.st, `INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
 INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('a','r','a','p','open','',0),('b','r','b','p','open','',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at,harness) VALUES('ba','a','','agent','ready','','',0,0,'agy'),('bb','b','','agent','ready','','',0,0,'agy');
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('t1','a',1,'running','m',0,0,0,0),('t2','b',1,'running','m',0,0,0,0)`)
	_, problem := r.use("New")
	if problem == nil || problem.Code != contract.ErrConflict || !strings.Contains(problem.Message, "2 agy turns") {
		t.Fatalf("running turns: %v", problem)
	}
	if u := r.state(); u.Held || u.Active != r.old || u.Target != "" || len(r.hosts.installs) != 0 || file(r.layout.HostAgyMainToken()) != tokOld {
		t.Fatalf("something changed: %+v", u)
	}
	// They end during the wait: the switch goes on.
	go func() {
		time.Sleep(60 * time.Millisecond)
		r.st.Write(r.ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE turns SET ended_at=2,outcome='completed'")
			return err
		})
	}()
	if _, problem := r.use("New"); problem != nil {
		t.Fatalf("turns that ended in time: %v", problem)
	}
}

// Seed is refused while a switch is held or running, and Remove refuses an account the switch uses.
func TestHeldSwitchRefusesRemovalOfItsAccountsAndShowsInTheList(t *testing.T) {
	r := newUseRig(t)
	if _, problem := r.actions.AccountsRemove(r.ctx, owner, contract.AccountsRemoveRequest{AccountID: r.old}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("removed the active account: %v", problem)
	}
	listed, problem := r.actions.AccountsList(r.ctx, owner, contract.AccountsListRequest{})
	if problem != nil || listed.AgySwitch != nil {
		t.Fatalf("list: %+v %v", listed, problem)
	}
	active := 0
	for _, a := range listed.Accounts {
		if a.Active {
			active++
			if a.ID != r.old {
				t.Fatalf("the wrong account is active: %v", a)
			}
		}
	}
	if active != 1 {
		t.Fatalf("%d active accounts", active)
	}
	r.hosts.failOn["node"] = errors.New("x")
	r.use("New")
	listed, _ = r.actions.AccountsList(r.ctx, owner, contract.AccountsListRequest{})
	if listed.AgySwitch == nil || listed.AgySwitch.Target != r.new || listed.AgySwitch.Host != "node" {
		t.Fatalf("the held switch is not shown: %+v", listed.AgySwitch)
	}
	if held, err := r.actions.service.AgySwitchHeld(r.ctx); err != nil || !held {
		t.Fatalf("held %v %v", held, err)
	}
	if _, problem := r.actions.AccountsRemove(r.ctx, owner, contract.AccountsRemoveRequest{AccountID: r.new}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("removed the target: %v", problem)
	}
}

func TestImportMainIsActiveAndAFileIsNotAndEveryUnsafeNameIsRefused(t *testing.T) {
	r := newAgyRig(t)
	ctx, layout := r.ctx, r.layout
	writeFile(t, layout.HostAgyMainToken(), tokNew)
	first := filepath.Join(layout.HostAgyAccounts(), "first.oauth-token.bak")
	writeFile(t, first, tokOld)
	writeFile(t, filepath.Join(layout.HostAgyAccounts(), "secret"), "OUTSIDE")
	// A limit that stood on "main" before accounts existed follows the imported account.
	sqlExec(t, r.st, "INSERT INTO agy_limits(account,until) VALUES('main',9999999999999)")
	out, problem := r.actions.AgyImport(ctx, owner, contract.AgyImportRequest{Label: " Second "})
	if problem != nil {
		t.Fatal(problem)
	}
	if file(layout.HostAgyToken(string(out.Account))) != tokNew {
		t.Fatal("main's token was not copied whole")
	}
	if info, _ := os.Stat(layout.HostAgyToken(string(out.Account))); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if u, _ := r.st.AgyUse(ctx); u.Active != out.Account || u.Held {
		t.Fatalf("state %+v", u)
	}
	if n := rows(t, r.st, "SELECT count(*) FROM agy_limits WHERE account=?", string(out.Account)); n != 1 {
		t.Fatalf("limit rows under the account: %d", n)
	}
	accounts, _ := r.st.AgyAccounts(ctx)
	if len(accounts) != 1 || accounts[0].Label != "Second" {
		t.Fatalf("accounts %+v", accounts)
	}
	// A second import of main is refused while an account is active.
	if _, problem := r.actions.AgyImport(ctx, owner, contract.AgyImportRequest{Label: "Again"}); problem == nil || problem.Code != contract.ErrConflict {
		t.Fatalf("second main import: %v", problem)
	}
	// A file is an account that is not active.
	file1, problem := r.actions.AgyImport(ctx, owner, contract.AgyImportRequest{Label: "First", File: "first.oauth-token.bak"})
	if problem != nil || file(layout.HostAgyToken(string(file1.Account))) != tokOld {
		t.Fatalf("file import: %v", problem)
	}
	if u, _ := r.st.AgyUse(ctx); u.Active != out.Account {
		t.Fatalf("a file import changed the active account: %+v", u)
	}
	// Names that are not plain names in the accounts folder, a link, a folder and a missing file.
	os.Symlink(filepath.Join(layout.HostAgyAccounts(), "secret"), filepath.Join(layout.HostAgyAccounts(), "link"))
	os.MkdirAll(filepath.Join(layout.HostAgyAccounts(), "dir"), 0o700)
	for _, name := range []string{"../first.oauth-token.bak", "main/antigravity-acp/antigravity-oauth-token", "/etc/passwd", "a\\b", ".", "..", "link", "dir", "missing", "x\x00y"} {
		if _, problem := r.actions.AgyImport(ctx, owner, contract.AgyImportRequest{Label: "X", File: name}); problem == nil {
			t.Fatalf("%q was imported", name)
		}
	}
	if accounts, _ := r.st.AgyAccounts(ctx); len(accounts) != 2 {
		t.Fatalf("a refused import left an account: %+v", accounts)
	}
	if left, _ := filepath.Glob(filepath.Join(layout.HostAgyTokens(), ".copy-*")); len(left) != 0 {
		t.Fatalf("half copies left: %v", left)
	}
	if _, problem := r.actions.AgyImport(ctx, contract.Principal{Kind: contract.PrincipalSession}, contract.AgyImportRequest{Label: "X"}); problem == nil || problem.Code != contract.ErrForbidden {
		t.Fatalf("a session imported: %v", problem)
	}
}

func TestImportOfMainThatKeepsChangingIsRefused(t *testing.T) {
	r := newAgyRig(t)
	writeFile(t, r.layout.HostAgyMainToken(), "ONE")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			os.WriteFile(r.layout.HostAgyMainToken(), []byte{byte('a' + i%26), byte('a' + i/26%26)}, 0o600)
		}
	}()
	_, problem := r.actions.AgyImport(r.ctx, owner, contract.AgyImportRequest{Label: "Live"})
	close(stop)
	<-done
	// A writer racing two copies may be missed by luck; what must hold is that a success is whole and a failure leaves nothing.
	accounts, _ := r.st.AgyAccounts(r.ctx)
	if problem != nil && len(accounts) != 0 {
		t.Fatalf("a refused import left %+v", accounts)
	}
	if left, _ := filepath.Glob(filepath.Join(r.layout.HostAgyTokens(), ".copy-*")); len(left) != 0 {
		t.Fatalf("half copies left: %v", left)
	}
}

// The sentinel in a token is in no event, no status and no output; it exists in token files only.
func TestNoTokenTextReachesAnEventOrAResponse(t *testing.T) {
	r := newUseRig(t)
	out, problem := r.use("New")
	if problem != nil {
		t.Fatal(problem)
	}
	imported, _ := r.actions.AgyImport(r.ctx, owner, contract.AgyImportRequest{Label: "x"})
	listed, _ := r.actions.AccountsList(r.ctx, owner, contract.AccountsListRequest{})
	events, _ := r.st.OwnerEvents(r.ctx, 0, 1000)
	raw, _ := json.Marshal([]any{out, imported, listed, events})
	for _, s := range []string{tokOld, tokNew, "SENTINEL"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Fatalf("%q reached a response or event", s)
		}
	}
}
