package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"github.com/resident-labs/dyna/engine/store"
)

func TestPrStackBoundary(t *testing.T) {
	for _, mode := range []string{"valid", "broken base", "unrelated owner", "fork", "already stacked", "short", "long", "duplicate", "closed", "wrong repo", "owner principal", "ancestor", "github error", "late persistence", "closed owner", "case collision"} {
		t.Run(mode, func(t *testing.T) {
			s, room, root, ctx := setup(t)
			if err := s.store.AddRoomRepo(ctx, room, "owner/repo", true); err != nil {
				t.Fatal(err)
			}
			lead, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: root, Name: "stack-lead", Profile: "test", Goal: "stack"})
			if e != nil {
				t.Fatal(e)
			}
			child, e := s.SessionStart(ctx, contract.Principal{Kind: contract.PrincipalOwner}, contract.SessionStartRequest{Room: room, Parent: lead.Session, Name: "stack-worker", Profile: "test", Goal: "stack"})
			if e != nil {
				t.Fatal(e)
			}
			caller := child.Session
			owner := caller
			if mode == "ancestor" {
				caller = lead.Session
			}
			if mode == "unrelated owner" {
				owner = root
			}
			for _, n := range []int{1, 2} {
				if err := s.store.TrackPR(ctx, store.TrackedPR{PrSummary: contract.PrSummary{Repo: "owner/repo", Number: n, Session: owner, State: contract.PrOpen}}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "closed owner" || mode == "case collision" {
				err := s.store.Write(ctx, func(tx *sql.Tx) error {
					query := "UPDATE sessions SET closed_at=1 WHERE id=?"
					if mode == "case collision" {
						query = "INSERT INTO pr_owners(repo,number,session,goal,approved_head) VALUES('Owner/Repo',2,?,'','')"
					}
					_, err := tx.ExecContext(ctx, query, owner)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}

			posts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
					t.Error("missing auth or version")
				}
				if r.Method == "POST" {
					posts++
					if r.URL.Path != "/repos/owner/repo/stacks" {
						t.Error(r.URL.Path)
					}
					var body struct {
						PRs []int `json:"pull_requests"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || fmt.Sprint(body.PRs) != "[1 2]" {
						t.Errorf("body %+v %v", body, err)
					}
					if mode == "github error" {
						w.WriteHeader(422)
						fmt.Fprint(w, `{"message":"secret denied"}`)
						return
					}
					if mode == "late persistence" {
						if err := s.store.UntrackPR(ctx, "owner/repo", 2); err != nil {
							t.Error(err)
						}
					}
					w.WriteHeader(201)
					fmt.Fprint(w, `{"number":50}`)
					return
				}
				if r.URL.Path == "/repos/owner/repo" {
					fmt.Fprint(w, `{"default_branch":"main"}`)
					return
				}
				n := 1
				if strings.HasSuffix(r.URL.Path, "/2") {
					n = 2
				}
				base := "main"
				if n == 2 {
					base = "layer1"
				}
				if mode == "broken base" && n == 2 {
					base = "bad"
				}
				repo := "owner/repo"
				if mode == "fork" {
					repo = "fork/repo"
				}
				state := "open"
				if mode == "closed" {
					state = "closed"
				}
				baseRepo := "owner/repo"
				if mode == "wrong repo" {
					baseRepo = "other/repo"
				}
				stack := "null"
				if mode == "already stacked" {
					stack = `{"number":9}`
				}
				fmt.Fprintf(w, `{"number":%d,"state":%q,"head":{"ref":"layer%d","repo":{"full_name":%q}},"base":{"ref":%q,"repo":{"full_name":%q}},"stack":%s}`, n, state, n, repo, base, baseRepo, stack)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			entry := s.PrStack("secret", endpoint, server.Client().Transport)
			if err := s.ConfigureOperations(ctx, map[contract.OpKind]approve.Entry{"pr.stack": entry}, time.Hour); err != nil {
				t.Fatal(err)
			}
			prs := []int{1, 2}
			switch mode {
			case "short":
				prs = []int{1}
			case "long":
				prs = make([]int, 21)
			case "duplicate":
				prs = []int{1, 1}
			}
			who := sessionCaller(caller)
			if mode == "owner principal" {
				who = contract.Principal{Kind: contract.PrincipalOwner}
			}
			if mode != "valid" && mode != "ancestor" && mode != "github error" && mode != "late persistence" {
				err := entry.Check(ctx, contract.PrStackArgs{PRs: prs}, who)
				if !errors.Is(err, approve.ErrInvalid) || !strings.Contains(err.Error(), "PR #") {
					t.Fatalf("invalid error: %v", err)
				}
			}
			result, failure := s.OpSubmit(ctx, who, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "pr.stack", Args: contract.PrStackArgs{PRs: prs}}})
			valid := mode == "valid" || mode == "ancestor" || mode == "github error" || mode == "late persistence"
			if !valid {
				if failure == nil {
					t.Fatalf("invalid chain accepted: %+v", result)
				}
				if posts != 0 {
					t.Fatal("invalid chain posted")
				}
				return
			}
			if failure != nil {
				t.Fatal(failure)
			}
			if posts != 1 {
				t.Fatalf("posts %d", posts)
			}
			if mode == "github error" {
				if result.Operation.State != contract.OpFailed || !strings.Contains(result.Operation.Result.Error, "denied") || strings.Contains(result.Operation.Result.Error, "secret") {
					t.Fatalf("%+v", result)
				}
				return
			}
			if result.Operation.State != contract.OpDone || result.Operation.Result.Stack == nil || result.Operation.Result.Stack.Stack != 50 {
				t.Fatalf("%+v", result)
			}
			if mode == "late persistence" {
				if !strings.Contains(result.Operation.Result.Output, "created") || !strings.Contains(result.Operation.Result.Output, "record") {
					t.Fatal(result.Operation.Result.Output)
				}
				p, _ := s.store.PR(ctx, "owner/repo", 1)
				if p.Stack != 0 {
					t.Fatal("partial write")
				}
				return
			}
			listed, fail := s.PrList(ctx, who, contract.PrListRequest{})
			if fail != nil || len(listed.PRs) != 2 {
				t.Fatalf("list %+v %v", listed, fail)
			}
			status, fail := s.PrStatus(ctx, who, contract.PrStatusRequest{Number: 2})
			if fail != nil || status.PR.Stack != 50 || status.PR.Position != 2 || status.PR.StackSize != 2 {
				t.Fatalf("status %+v %v", status, fail)
			}
			for _, p := range listed.PRs {
				if p.Stack != 50 || p.StackSize != 2 {
					t.Fatalf("list %+v", listed)
				}
			}
			for i, n := range prs {
				p, err := s.store.PR(ctx, "owner/repo", n)
				if err != nil || p.Stack != 50 || p.Position != i+1 || p.StackSize != 2 || p.Session != owner {
					t.Fatalf("persisted %+v %v", p, err)
				}
			}
		})
	}
}
