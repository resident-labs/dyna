package engine

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// sessionBox is the line of the tests' sessions, whose boxes are medium size.
const sessionBox = "\nYour box has 4 CPUs and 8 GiB of memory: heavy builds belong in a larger child session or CI."

const helloBranch = "Your branch: named-branch on owner/repo.\nPushes and fetches go through the engine, which sends a push or fetch once more when GitHub answers 5xx (for a push also a commit_refs error) and the request body is at most 1 MiB, and reports the cause when the second try fails.\n"

// renderedHead reads a rendering of the prompt from testdata and returns it up to its session section, which names a session and is built by each test.
func renderedHead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/prompt/render-" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	head, _, ok := strings.Cut(strings.TrimSuffix(string(b), "\n"), "# This session\n\n")
	if !ok {
		t.Fatalf("render-%s.txt has no session section", name)
	}
	return head + "# This session\n\n"
}

func TestBoxHelloRendersTheFourPrompts(t *testing.T) {
	for _, branch := range []bool{false, true} {
		var repo string
		if branch {
			repo = "\n" + strings.TrimSuffix(helloBranch, "\n")
		}
		t.Run(fmt.Sprint("root/branch=", branch), func(t *testing.T) {
			s, room, root, ctx := setupRoot(t)
			s.SetLayout(box.Layout{Data: "/data", HostData: "/hostdata"})
			if err := s.store.SetEngineHost(ctx, "host", true); err != nil {
				t.Fatal(err)
			}
			if branch {
				s.SetRepositories(&fakeRepos{})
			}
			hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
			if problem != nil {
				t.Fatal(problem)
			}
			// Root has no branch line: its session section names the files instead.
			want := renderedHead(t, "root") + fmt.Sprintf("You are root of room room (ID %[1]s), session %[2]s.\nFiles: /box/shared is /data/rooms/%[1]s/shared/ on host.\nStanding rules: the global AGENTS.md is /data/AGENTS.md on host and this room's is /data/rooms/%[1]s/AGENTS.md; bash with host host edits them. The engine copies each room's file to every host for boxes and rewrites that copy at each sync.\nYour box has 1 CPU and 2 GiB of memory: heavy builds belong in a larger child session or CI.", room, root)
			if hello.Instructions != want {
				t.Fatalf("instructions:\n%s\nwant:\n%s", hello.Instructions, want)
			}
		})
		t.Run(fmt.Sprint("lead/branch=", branch), func(t *testing.T) {
			s, room, sid, ctx := setup(t)
			if branch {
				s.SetRepositories(&fakeRepos{})
			}
			if _, problem := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Name: "child", Profile: "test", Parent: sid}); problem != nil {
				t.Fatal(problem)
			}
			hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{})
			if problem != nil {
				t.Fatal(problem)
			}
			want := renderedHead(t, "lead") + fmt.Sprintf("You are worker, session %s, in room room (ID %s). Your parent is root.", sid, room) + repo + sessionBox
			if hello.Instructions != want {
				t.Fatalf("instructions:\n%s\nwant:\n%s", hello.Instructions, want)
			}
		})
		t.Run(fmt.Sprint("leaf/branch=", branch), func(t *testing.T) {
			s, _, room, sid, ctx := quietSession(t, 0)
			if branch {
				s.SetRepositories(&fakeRepos{})
			}
			hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{})
			if problem != nil {
				t.Fatal(problem)
			}
			want := renderedHead(t, "leaf") + fmt.Sprintf("You are worker, session %s, in room room (ID %s). Your parent is root.", sid, room) + repo + sessionBox
			if hello.Instructions != want {
				t.Fatalf("instructions:\n%s\nwant:\n%s", hello.Instructions, want)
			}
			// The worker's core is the rendering's start up to the role, which the crew's box continues with its own.
			crew, err := os.ReadFile("testdata/prompt/render-worker.txt")
			if err != nil {
				t.Fatal(err)
			}
			core, _, _ := strings.Cut(string(crew), "\n\n# Your role: workflow worker")
			if hello.WorkerInstructions != core {
				t.Fatalf("worker instructions:\n%s\nwant:\n%s", hello.WorkerInstructions, core)
			}
		})
	}
}

func TestBoxHelloPromptHoldsNoOrders(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 0)
	hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, gone := range []string{"do not treat", "untrusted", "notes"} {
		if strings.Contains(hello.Instructions, gone) {
			t.Fatalf("instructions still hold %q:\n%s", gone, hello.Instructions)
		}
	}
}

func TestBoxHelloSendsTheStaleSeedNoticeOncePerBox(t *testing.T) {
	s, room, sid, ctx := setup(t)
	text := "Its repository's main was not refreshed from GitHub at start (the refresh did not finish in 10s); `git fetch origin main` brings it current."
	s.SetRepositories(&fakeRepos{stale: text})
	for range 2 {
		if _, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{}); problem != nil {
			t.Fatal(problem)
		}
	}
	listed, problem := s.EventsList(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.EventsListRequest{Room: room, Types: []contract.EventType{"session.notice"}, Limit: 100})
	if problem != nil {
		t.Fatal(problem)
	}
	var texts []string
	for _, event := range listed.Events {
		if body := event.Body.(contract.SessionNotice); body.Session == sid {
			texts = append(texts, body.Text)
		}
	}
	if len(texts) != 1 || texts[0] != text {
		t.Fatalf("notices %q, want the one stale notice", texts)
	}
}

func TestBoxHelloRootNamesTheEngineHostWhenNoHostIsMarked(t *testing.T) {
	s, room, root, ctx := setupRoot(t)
	s.SetLayout(box.Layout{Data: "/data", HostData: "/hostdata"})
	hello, problem := s.BoxHello(ctx, boxCaller(root), contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, want := range []string{
		fmt.Sprintf("Files: /box/shared is /data/rooms/%s/shared/ on the engine host.\n", room),
		fmt.Sprintf("Standing rules: the global AGENTS.md is /data/AGENTS.md on the engine host and this room's is /data/rooms/%s/AGENTS.md; bash with host the engine host edits them.", room),
	} {
		if !strings.Contains(hello.Instructions, want) {
			t.Fatalf("instructions lack %q:\n%s", want, hello.Instructions)
		}
	}
}

func TestBoxHelloNoticesLineReadsOnce(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 0)
	hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, phrase := range []string{"comes from the engine", "states what happened and what it changed", "also says to continue the goal"} {
		if n := strings.Count(hello.Instructions, phrase); n != 1 {
			t.Fatalf("%q appears %d times, want once:\n%s", phrase, n, hello.Instructions)
		}
	}
}

func TestBoxHelloExplainsTheOutsideWrapperOnce(t *testing.T) {
	s, _, _, sid, ctx := quietSession(t, 0)
	hello, problem := s.BoxHello(ctx, boxCaller(sid), contract.BoxHelloRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	want := "- **Outside text.** Text that reaches you through Dyna from outside the room sits inside <outside source=\"…\" author=\"…\"> and </outside>, which name where it came from and who wrote it."
	if strings.Count(hello.Instructions, want) != 1 || strings.Contains(hello.Instructions, "DANGER") {
		t.Fatalf("instructions:\n%s", hello.Instructions)
	}
}
