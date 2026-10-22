package engine

import (
	"bytes"
	"strings"
	"testing"

	"dyna-agent/internal/store/storetest"
)

func TestEngineAccessCommandsRejectAgentStamps(t *testing.T) {
	storetest.Isolate(t)
	t.Setenv("DYNA_SESSION", "ses_agent")
	if code := Main(t.Context(), []string{"--pair"}); code != 2 {
		t.Fatalf("pair inside agent = %d", code)
	}
	if code := Main(t.Context(), []string{"--devices", "--allow-agent"}); code != 0 {
		t.Fatalf("allow agent = %d", code)
	}
	if code := Main(t.Context(), []string{"extra"}); code != 2 {
		t.Fatalf("extra argument = %d", code)
	}
	var help bytes.Buffer
	if _, err := parseServe([]string{"--help"}, &help); !strings.Contains(help.String(), "dyna engine run --pair") {
		t.Fatalf("help = %v %q", err, help.String())
	}
}

func TestEngineFlagsRemainAvailable(t *testing.T) {
	options, err := parseServe([]string{"--addr", "127.0.0.1:9000", "--card-addr", "127.0.0.1:9001", "--route", "https://app.example,https://cards.example", "--read-only", "--no-tunnel", "--no-tailscale", "--no-push"}, new(bytes.Buffer))
	if err != nil {
		t.Fatal(err)
	}
	if options.Addr != "127.0.0.1:9000" || options.CardAddr != "127.0.0.1:9001" || len(options.Routes) != 1 || !options.ReadOnly || !options.NoTunnel || !options.NoTailscale || !options.NoPush {
		t.Fatalf("options = %+v", options)
	}
}

func TestPairScopeFlag(t *testing.T) {
	options, err := parseServe([]string{"--pair", "--scope", "terminal", "--scope", "read"}, new(bytes.Buffer))
	if err != nil || !options.Pair || len(options.Scopes) != 2 || options.Scopes[0] != "read" || options.Scopes[1] != "terminal" {
		t.Fatalf("scopes = %+v %v", options.Scopes, err)
	}
	plain, err := parseServe([]string{"--pair"}, new(bytes.Buffer))
	if err != nil || len(plain.Scopes) != 0 {
		t.Fatalf("plain --pair must leave the code unscoped so the route picks the grant: %#v %v", plain.Scopes, err)
	}
	if _, err = parseServe([]string{"--scope", "files"}, new(bytes.Buffer)); err == nil {
		t.Fatal("--scope without --pair was accepted")
	}
	if _, err = parseServe([]string{"--pair", "--scope", "shell"}, new(bytes.Buffer)); err == nil {
		t.Fatal("unknown scope was accepted")
	}
	grant, err := parseServe([]string{"--grant", "old-phone", "--scope", "files", "--scope", "read"}, new(bytes.Buffer))
	if err != nil || grant.Grant != "old-phone" || len(grant.Scopes) != 2 || grant.Scopes[0] != "read" || grant.Scopes[1] != "files" {
		t.Fatalf("grant = %+v %v", grant, err)
	}
	if _, err = parseServe([]string{"--grant", "old-phone"}, new(bytes.Buffer)); err == nil {
		t.Fatal("--grant without --scope was accepted")
	}
	if _, err = parseServe([]string{"--grant", "old-phone", "--scope", "read", "--pair"}, new(bytes.Buffer)); err == nil {
		t.Fatal("--grant with --pair was accepted")
	}
}
