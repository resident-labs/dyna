package authority

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/store/storetest"
)

func TestRulesRoles(t *testing.T) {
	for _, rule := range Rules {
		for _, role := range []Role{Owner, Root, Session, Worker} {
			allowed := false
			for _, want := range rule.Allowed {
				allowed = allowed || want == role
			}
			err := Check(rule.Action, Actor{Role: role}, nil)
			if (err == nil) != allowed {
				t.Errorf("%s %s: %v", rule.Action, role, err)
			}
			if err != nil && !strings.HasPrefix(err.Error(), rule.Action+" is ") {
				t.Errorf("refusal: %v", err)
			}
		}
	}
	if err := Check("missing action", Actor{Role: Owner}, nil); err == nil {
		t.Fatal("unknown action allowed")
	}
}

func TestRelations(t *testing.T) {
	Rules = append(Rules, Rule{Action: "test relation", Relations: []Relation{Self, Parent}})
	defer func() { Rules = Rules[:len(Rules)-1] }()
	for _, relation := range []Relation{Self, Parent} {
		err := Check("test relation", Actor{Role: Session}, func(got Relation) bool { return got == relation })
		if err != nil {
			t.Fatal(err)
		}
	}

}

func TestAskHelpAndGuide(t *testing.T) {
	if got := Who("ask"); got != "Who: root only." {
		t.Fatal(got)
	}
	if Who("unknown") != "" {
		t.Fatal("unknown help")
	}
	for _, rule := range Rules {
		if !strings.Contains(Guide(), "`"+rule.Action+"`") {
			t.Fatal(rule.Action)
		}
	}
}

func TestCurrent(t *testing.T) {
	for _, tc := range []struct {
		mode string
		role Role
	}{{"", Owner}, {"root", Root}, {"subagent", Session}, {"workflow", Worker}} {
		t.Run(tc.mode, func(t *testing.T) {
			t.Setenv("DYNA_MODE", tc.mode)
			session, room := "ses-test", "room-test"
			if tc.role == Owner {
				session, room = "", ""
			}
			t.Setenv("DYNA_SESSION", session)
			run, agent := "", ""
			if tc.role == Worker {
				run, agent = "run_a", "1"
			}
			t.Setenv("DYNA_WORKER_RUN", run)
			t.Setenv("DYNA_WORKER_AGENT", agent)
			t.Setenv("DYNA_ROOM_ID", room)
			t.Setenv("DYNA_ROOM", "")
			if actor := Current(); actor.Role != tc.role || actor.Session != session || actor.Room != room {
				t.Fatalf("actor = %+v", actor)
			}
		})
	}
	t.Setenv("DYNA_ROOM_ID", "")
	t.Setenv("DYNA_ROOM", "named-room")
	if Current().Room != "named-room" {
		t.Fatal(Current())
	}
}

func TestRoleLimits(t *testing.T) {
	for _, tc := range []struct {
		action  string
		allowed []Role
	}{
		{"call", []Role{Root}},
		{"call list", []Role{Root}},
		{"call withdraw", []Role{Root}},
		{"ask", []Role{Root}},
		{"ask withdraw", []Role{Root, Session, Worker}},
		{"room create", []Role{Owner, Root}},
		{"room edit", []Role{Owner, Root}},
		{"room delete", []Role{Owner, Root}},
		{"room watch", []Role{Owner}},
		{"root attach", []Role{Owner}},
		{"root detach", []Role{Owner}},
		{"subagent start", []Role{Owner, Root, Session}},
		{"doctor examine", []Role{Owner, Root, Session}},
		{"doctor report", []Role{Owner, Root, Session}},
	} {
		for _, role := range []Role{Owner, Root, Session, Worker} {
			want := false
			for _, allowed := range tc.allowed {
				want = want || allowed == role
			}
			if err := Check(tc.action, Actor{Role: role}, nil); (err == nil) != want {
				t.Errorf("%s as %s: %v", tc.action, role, err)
			}
		}
	}
	for _, rule := range Rules {
		if (strings.HasPrefix(rule.Action, "subagent ") && rule.Action != "subagent dismiss-pending" && rule.Action != "subagent close" && rule.Action != "subagent promote" && rule.Action != "subagent adopt" && rule.Action != "subagent rename") || rule.Action == "room pause" || rule.Action == "room resume" || rule.Action == "run" || rule.Action == "demo" {
			for _, role := range []Role{Owner, Root, Session, Worker} {
				if err := Check(rule.Action, Actor{Role: role}, nil); (err == nil) != (role != Worker) {
					t.Errorf("%s as %s: %v", rule.Action, role, err)
				}
			}
		}
	}
}

// Inside the engine, the process's own environment is the owner's, never a
// caller's. A context without an actor, for example one a goroutine or a
// timer dropped, must fail every check (requirements item 3).
func TestEngineRefusesAContextWithoutAnActor(t *testing.T) {
	storetest.ClearStamp(t)
	if got := Of(context.Background()); got.Role != Owner {
		t.Fatalf("outside the engine, a bare context is %+v, want the process's own owner", got)
	}
	t.Cleanup(InEngine())
	for _, rule := range Rules {
		if err := Check(rule.Action, Of(context.Background()), func(Relation) bool { return true }); err == nil {
			t.Errorf("in the engine, a bare context passed %s", rule.Action)
		}
	}
	if err := Check("room delete", Of(context.Background()), nil); err == nil || err.Error() != "room delete needs a caller, and this call has none" {
		t.Fatalf("in the engine, a bare context gave %v", err)
	}
	owner := WithActor(context.Background(), Actor{Role: Owner})
	if err := Check("room delete", Of(owner), nil); err != nil {
		t.Fatalf("in the engine, a stated owner was refused: %v", err)
	}
}

// A workflow worker's actor names its run and agent, so checks and logs can
// say which run acted.
func TestWorkerActorNamesItsRunAndAgent(t *testing.T) {
	t.Setenv(mode.EnvMode, mode.Workflow)
	t.Setenv(mode.EnvSession, "")
	t.Setenv(mode.EnvWorkerRun, "run_a")
	t.Setenv(mode.EnvWorkerAgent, "3")
	if got := Current(); got.Role != Worker || got.Run != "run_a" || got.Agent != 3 {
		t.Fatalf("Current() = %+v, want worker 3 of run_a", got)
	}
	for _, env := range [][2]string{{"", "3"}, {"run_a", "x"}} {
		t.Setenv(mode.EnvWorkerRun, env[0])
		t.Setenv(mode.EnvWorkerAgent, env[1])
		if got := Current(); got.Role != "" {
			t.Errorf("a worker stamped run %q agent %q is %+v, want no role", env[0], env[1], got)
		}
	}
	if got, err := WorkerActor("run_a", 0); err != nil || got != (Actor{Role: Worker, Run: "run_a"}) {
		t.Fatalf("the run itself = %+v, %v", got, err)
	}
	for _, bad := range []struct {
		run   string
		agent int
	}{{"", 1}, {"run_a", -1}} {
		if _, err := WorkerActor(bad.run, bad.agent); err == nil {
			t.Errorf("WorkerActor(%q, %d) was accepted", bad.run, bad.agent)
		}
	}
}

func TestActorInUsesTheCallersRoom(t *testing.T) {
	for _, tc := range []struct {
		name  string
		actor Actor
		room  string
		allow bool
	}{
		{"owner in any room", Actor{Role: Owner}, "other", true},
		{"root in its room", Actor{Role: Root, Session: "root", Room: "own"}, "own", true},
		{"root in another room", Actor{Role: Root, Session: "root", Room: "own"}, "other", false},
		{"session in its room", Actor{Role: Session, Session: "session", Room: "own"}, "own", true},
		{"worker in another room", Actor{Role: Worker, Run: "run", Room: "own"}, "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actor, err := ActorIn(WithActor(t.Context(), tc.actor), tc.room)
			if (err == nil) != tc.allow {
				t.Fatalf("ActorIn = %+v, %v", actor, err)
			}
			if tc.allow && actor != tc.actor {
				t.Fatalf("actor changed: %+v", actor)
			}
		})
	}
}
