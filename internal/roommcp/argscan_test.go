package roommcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	accountstore "dyna-agent/internal/account/store"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

func TestScanArguments(t *testing.T) {
	hash, words := termHash("Project Nightjar")
	in := scanInput{
		Identities: Identities{
			Personal: []string{"Jane Doe", "jane@example.com"},
			Logins:   []string{"jane-d", "fork-bot"},
			Secrets:  []string{"sekret-value-12345"},
			Home:     "/home/jane",
		},
		AllowedLogins: []string{"Fork-Bot"},
		BlockTerms:    []blockTerm{{hash, words}},
	}
	tests := []struct {
		name string
		args string
		rule Rule
	}{
		{"normal search", `{"query":"label:bug is:open","limit":10}`, ""},
		{"commit hash", `{"ref":"3f786850e387550fdab836ed7e6dc881de23001b"}`, ""},
		{"url path", `{"url":"https://example.com/home/docs"}`, ""},
		{"a similar name", `{"query":"Janet Doe"}`, ""},
		{"allowed login, any case", `{"assignee":"FORK-BOT"}`, ""},
		{"git user.name", `{"query":"fix by jane   DOE"}`, RulePersonal},
		{"email", `{"query":"mail jane@example.com now"}`, RulePersonal},
		{"email in a key", `{"jane@example.com":1}`, RulePersonal},
		{"escaped email", `{"query":"jane@example.com"}`, RulePersonal},
		{"known login", `{"assignee":"jane-d"}`, RuleLogin},
		{"block term", `{"query":"about project nightjar"}`, RuleBlockTerm},
		{"saved secret", `{"note":"x sekret-value-12345 y"}`, RuleSecret},
		{"github token", `{"note":"ghp_abcdefghijklmnopqrstuvwxyz0123456789"}`, RuleTokenShape},
		{"api key", `{"note":"sk-abcdefghijklmnopqrstuvwxyz0123"}`, RuleTokenShape},
		{"jwt", `{"note":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghij"}`, RuleTokenShape},
		{"random run", `{"note":"Zk3Qx9Lm2Vb7Nc4Rt8Yp1Wd6Hf5Gj0Sa"}`, RuleTokenShape},
		{"home path", `{"path":"/home/jane/work"}`, RulePath},
		{"local path", `{"path":"see /etc/passwd"}`, RulePath},
		{"dyna id", `{"note":"ses_20261029-153248-37d02b51"}`, RuleDynaID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rule, flagged, err := scanArguments(json.RawMessage(tc.args), in)
			if err != nil {
				t.Fatal(err)
			}
			if flagged != (tc.rule != "") || rule != tc.rule {
				t.Fatalf("scan = %q, %v; want %q", rule, flagged, tc.rule)
			}
		})
	}
	if _, _, err := scanArguments(json.RawMessage(`{"a":`), in); err == nil {
		t.Fatal("broken JSON passed the scan")
	}
}

// A blocked call sends no request, records no arguments, and the error names
// the rule and never the value.
func TestBlockedCallSendsNothing(t *testing.T) {
	f := newFixture(t)
	f.ready(t)
	f.ids = Identities{Personal: []string{"Jane Doe"}, Logins: []string{"jane-d", "fork-bot"}}
	if err := f.svc.BlockTerm(owner, f.room.ID, "board", "Project Nightjar", false); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.AllowLogin(owner, f.room.ID, "board", "Fork-Bot", false); err != nil {
		t.Fatal(err)
	}
	session := f.as(authority.Session, "ses_a", f.room)
	before := f.server.count("tools/call")
	for _, args := range []string{`{"q":"Jane Doe"}`, `{"q":"jane-d"}`, `{"q":"project nightjar"}`} {
		_, err := f.svc.Call(session, "board", "list_issues", json.RawMessage(args))
		if err == nil || !strings.Contains(err.Error(), "nothing was sent") {
			t.Fatalf("%s: %v", args, err)
		}
		for _, secret := range []string{"Jane", "jane-d", "nightjar"} {
			if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(secret)) {
				t.Fatalf("the error echoes %q: %v", secret, err)
			}
		}
	}
	if f.server.count("tools/call") != before {
		t.Fatal("a blocked call reached the server")
	}
	events := f.events(t)
	for _, ev := range events {
		if ev.Outcome != room.MCPFlagged || ev.Arguments != "" {
			t.Fatalf("event = %+v; want flagged with no arguments", ev)
		}
	}
	if len(events) != 3 {
		t.Fatalf("%d events, want one per blocked call", len(events))
	}
	// The allowed fork login and a plain query pass.
	for _, args := range []string{`{"assignee":"fork-bot"}`, `{"q":"label:bug"}`} {
		if _, err := f.svc.Call(session, "board", "list_issues", json.RawMessage(args)); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
	}
}

func TestBlockTermsAreStoredHashed(t *testing.T) {
	f := newFixture(t)
	f.ready(t)
	if err := f.svc.BlockTerm(owner, f.room.ID, "board", "Project Nightjar", false); err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := f.svc.db.QueryRow("SELECT hash FROM room_mcp_block_terms").Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if want, _ := termHash("project nightjar"); hash != want || strings.Contains(strings.ToLower(hash), "nightjar") {
		t.Fatalf("stored %q", hash)
	}
	if err := f.svc.BlockTerm(owner, f.room.ID, "nope", "x term", true); err == nil {
		t.Fatal("removing a term of an unknown server succeeded")
	}
}

func TestFailedIdentityReadStopsTheCall(t *testing.T) {
	f := newFixture(t)
	f.ready(t)
	f.svc.identities = func(context.Context, room.Record) (Identities, error) { return Identities{}, os.ErrPermission }
	before := f.server.count("tools/call")
	if _, err := f.svc.Call(f.as(authority.Session, "ses_a", f.room), "board", "list_issues", json.RawMessage(`{}`)); err == nil {
		t.Fatal("call ran without the identity check")
	}
	if f.server.count("tools/call") != before {
		t.Fatal("request sent")
	}
}

func TestAgentToolsListOnlyAllowedToolsOfGrantedServers(t *testing.T) {
	f := newFixture(t)
	f.ready(t)
	session := f.as(authority.Session, "ses_a", f.room)
	out, err := f.svc.AgentTools(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "list_issues") || strings.Contains(out, "claim_issue") || !strings.Contains(out, "<external_data") {
		t.Fatalf("tools = %s", out)
	}
	if ok, err := f.svc.Access(f.as(authority.Session, "ses_b", f.room)); err != nil || ok {
		t.Fatalf("ungranted session access = %v, %v", ok, err)
	}
	if ok, err := f.svc.Access(session); err != nil || !ok {
		t.Fatalf("granted session access = %v, %v", ok, err)
	}
}

func TestHostIdentities(t *testing.T) {
	repo := t.TempDir()
	accounts := &accountstore.Store{Root: filepath.Join(t.TempDir(), "accounts")}
	if err := accounts.Update(func(st *accountstore.State) error {
		st.Entries = append(st.Entries, accountstore.Entry{
			Metadata:   accountstore.Metadata{ID: "acct_1", Provider: accountstore.ProviderBrave, Harness: "pi", Identity: "jane@work.example"},
			Credential: accountstore.Credential{Schema: "x", Payload: json.RawMessage(`{"apiKey":"brave-key-0123456789","region":"us-east-long-name"}`)},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	host := Host{Accounts: accounts, Home: "/home/jane", Run: func(_ context.Context, dir, name string, args ...string) (string, error) {
		switch strings.Join(append([]string{name}, args...), " ") {
		case "git config --get user.name":
			return "Jane Doe\n", nil
		case "git config --get user.email":
			return "jane@example.com\n", nil
		case `git config --get-regexp ^remote\..*\.url$`:
			return "remote.origin.url git@github.com:acme-org/app.git\n", nil
		case "gh auth status --json hosts":
			return `{"hosts":{"github.com":[{"login":"jane-d"}]}}`, nil
		}
		return "", os.ErrNotExist
	}}
	ids, err := host.Identities(context.Background(), room.Record{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	has := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"Jane Doe", "jane@example.com", "jane@work.example"} {
		if !has(ids.Personal, want) {
			t.Errorf("personal %v lacks %q", ids.Personal, want)
		}
	}
	for _, want := range []string{"acme-org", "jane-d"} {
		if !has(ids.Logins, want) {
			t.Errorf("logins %v lacks %q", ids.Logins, want)
		}
	}
	if !has(ids.Secrets, "brave-key-0123456789") || has(ids.Secrets, "us-east-long-name") {
		t.Errorf("secrets = %v; want the key and not the region", ids.Secrets)
	}
	if ids.Home != "/home/jane" {
		t.Errorf("home = %q", ids.Home)
	}
}
