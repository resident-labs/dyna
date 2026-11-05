package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/resident-labs/dyna/contract"
	"os"
	"testing"
	"time"
)

func TestWaitingReasonsAndClearing(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateRoom(ctx, "r", "r", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err = s.writer.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent) VALUES('root','r','root','p','open','',0,NULL),('a','r','a','p','open','',1,'root'),('b','r','b','p','open','',2,'a'),('c','r','c','p','open','',3,'b'); INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) SELECT 'box-'||id,id,'','agent','ready','','',0,0 FROM sessions;`)
	reason := func(id contract.SessionID) *contract.SessionWaiting {
		t.Helper()
		list, err := s.SessionList(ctx, contract.SessionListRequest{Room: "r"}, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range list.Sessions {
			if v.ID == id {
				return v.Waiting
			}
		}
		t.Fatal("missing session")
		return nil
	}
	if got := reason("a"); got != nil {
		t.Fatalf("idle: %+v", got)
	}
	exec(`INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('tc','c',10,'','m',0,0,0,0)`)
	if got := reason("a"); got == nil || got.Kind != contract.WaitChildren || got.Name != "b" || got.Count != 1 {
		t.Fatalf("descendant: %+v", got)
	}
	if got := reason("c"); got != nil {
		t.Fatalf("own turn: %+v", got)
	}
	exec(`UPDATE sessions SET shell_jobs=2 WHERE id='a'`)
	if got := reason("a"); got == nil || got.Kind != contract.WaitJobs || got.Count != 2 {
		t.Fatalf("jobs precedence: %+v", got)
	}
	exec(`UPDATE turns SET ended_at=20,outcome='stopped'; UPDATE sessions SET shell_jobs=0`)
	if got := reason("a"); got != nil {
		t.Fatalf("cleared chain: %+v", got)
	}
	children, err := s.ContextChildren(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if children[0].Waiting != nil {
		t.Fatal("context stale")
	}
	exec(`UPDATE sessions SET shell_jobs=2 WHERE id='a'`)
	children, err = s.ContextChildren(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	if children[0].Waiting == nil || children[0].Waiting.Kind != contract.WaitJobs {
		t.Fatalf("context differs: %+v", children)
	}
	exec(`INSERT INTO questions(id,room,session,text,state,asked_at) VALUES('q','r','a','help','pending',1)`)
	if got := reason("a"); got == nil || got.Kind != contract.WaitOwner {
		t.Fatalf("owner priority: %+v", got)
	}
	exec(`UPDATE questions SET state='answered'; INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('op','hostexec','r','session','a','owner','pending','{}','{}','',0,100)`)
	if got := reason("a"); got == nil || got.Kind != contract.WaitOwner {
		t.Fatalf("approval: %+v", got)
	}
	exec(`UPDATE operations SET state='approved'; UPDATE sessions SET shell_jobs=0; INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at) VALUES('wf','r','b','audit','','','{}','[]','','','','running',1)`)
	if got := reason("a"); got == nil || got.Kind != contract.WaitWorkflow || got.Name != "audit" {
		t.Fatalf("workflow: %+v", got)
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		sessions, _, err := s.readCheckinRoom(ctx, tx, "r")
		if err != nil {
			return err
		}
		for _, f := range sessions {
			if f.ID == "a" && !f.Pending {
				t.Error("workflow wait would be nagged as idle")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('ta','a',30,'','m',0,0,0,0)`)
	if got := reason("a"); got != nil {
		t.Fatalf("own turn takes precedence: %+v", got)
	}
	exec(`UPDATE turns SET ended_at=40,outcome='completed'; UPDATE workflow_runs SET state='done'`)
	if got := reason("a"); got != nil {
		t.Fatalf("finished workflow: %+v", got)
	}
}

// This is also the typed session.list fixture used by the browser check. It is
// emitted only when requested; the assertions run in the ordinary suite too.
func TestWaitingVisualFixture(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if err = s.CreateRoom(ctx, "r", "Background work", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	_, err = s.writer.Exec(`INSERT INTO sessions(id,room,name,profile,state,goal,created_at,parent,shell_jobs) VALUES
 ('root','r','build-lead','sol','open','',1,NULL,0),
 ('jobs','r','background-builder','sol','open','',2,'root',2),
 ('lead','r','services-lead','sol','open','',3,'root',0),
 ('worker','r','services-core','sol','open','',4,'lead',0),
 ('starter','r','audit-lead','sol','open','',5,'root',0),
 ('crew','r','audit-crew','sol','open','',6,'starter',0),
 ('ask','r','release-check','sol','open','',7,'root',0),
 ('idle','r','finished-reader','sol','open','',8,'root',0),
 ('failed','r','broken-build','sol','open','',9,'root',0);
 INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) SELECT 'box-'||id,id,'','agent',CASE WHEN id='failed' THEN 'failed' ELSE 'ready' END,'','',0,0 FROM sessions;
 INSERT INTO turns(id,session,started_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('tr','root',10,'','m',0,0,0,0),('tw','worker',10,'','m',0,0,0,0);
 INSERT INTO operations(id,kind,room,by_kind,by_id,class,state,card,args,args_sha256,created_at,expires_at) VALUES('op','hostexec','r','session','ask','owner','pending','{}','{}','',0,100);
 INSERT INTO workflow_runs(id,room,session,name,description,script,args,profiles,repo,commit_sha,image,state,started_at) VALUES('wf','r','crew','review-sweep','','','{}','[]','','','','running',1);`)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.SessionList(ctx, contract.SessionListRequest{Room: "r"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	waits := map[contract.SessionID]contract.WaitKind{}
	for _, item := range list.Sessions {
		if item.Waiting != nil {
			waits[item.ID] = item.Waiting.Kind
		}
	}
	if waits["jobs"] != contract.WaitJobs || waits["lead"] != contract.WaitChildren || waits["starter"] != contract.WaitWorkflow || waits["ask"] != contract.WaitOwner || waits["root"] != "" {
		t.Fatalf("fixture waits %+v", waits)
	}
	if path := os.Getenv("DYNA_WAITING_FIXTURE"); path != "" {
		data, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
