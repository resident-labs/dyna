package githubmcp

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/resident-labs/dyna/engine/githubaccount"
	"github.com/resident-labs/dyna/engine/store"
)

func TestRoomAccountMCPReadAuthorization(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateRoom(ctx, "work", "work", time.Now()); err != nil {
		t.Fatal(err)
	}
	files := &githubaccount.Files{Dir: t.TempDir()}
	if err := files.Write("key", "work-secret"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGitHubAccount(ctx, "work", store.GitHubIdentity{Login: "work", Credential: "key"}); err != nil {
		t.Fatal(err)
	}
	resolver := &githubaccount.Resolver{Store: st, Tokens: files, DefaultToken: "engine-secret"}
	token, _, err := resolver.Room(ctx, "work")
	if err != nil {
		t.Fatal(err)
	}
	fake := httptest.NewServer(&fakeGitHub{remaining: 4999, reset: time.Now().Add(time.Hour)})
	defer fake.Close()
	target, _ := url.Parse(fake.URL)
	transport := &toFake{target: target}
	handler, err := Handler(ctx, token, transport, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "list_branches", Arguments: map[string]any{"owner": "o", "repo": "r"}})
	if err != nil || result.IsError {
		t.Fatalf("read: %v %v", result, err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.auth) == 0 {
		t.Fatal("no GitHub reads")
	}
	for _, auth := range transport.auth {
		if !strings.HasSuffix(auth, "Bearer work-secret") || strings.Contains(auth, "engine-secret") {
			t.Fatalf("wrong authorization: %q", auth)
		}
	}
}
