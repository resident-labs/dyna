package githubaccount

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

type metadata struct {
	account Account
	err     error
}

func (m metadata) GitHubAccount(context.Context, contract.RoomID) (Account, error) {
	return m.account, m.err
}
func (m metadata) RepoRoom(context.Context, contract.RepoName) (contract.RoomID, error) {
	return "room", m.err
}

func TestResolutionNeverFallsBack(t *testing.T) {
	files := &Files{Dir: t.TempDir()}
	resolver := &Resolver{Store: metadata{}, Tokens: files, DefaultToken: "engine-secret"}
	token, login, err := resolver.Room(context.Background(), "room")
	if err != nil || token != "engine-secret" || login != "" {
		t.Fatalf("default account: %q %q %v", token, login, err)
	}
	resolver.Store = metadata{account: Account{Login: "work"}}
	token, login, err = resolver.Room(context.Background(), "room")
	if err == nil || token != "" || login != "work" || !strings.Contains(err.Error(), "GitHub account work of room room") {
		t.Fatalf("missing account token: %q %q %v", token, login, err)
	}
	if err := files.Write("room", "room-secret"); err != nil {
		t.Fatal(err)
	}
	token, login, err = resolver.Repo(context.Background(), "owner/repo")
	if err != nil || token != "room-secret" || login != "work" {
		t.Fatalf("repository account: %q %q %v", token, login, err)
	}
	resolver.Store = metadata{err: errors.New("metadata unavailable")}
	token, _, err = resolver.Room(context.Background(), "room")
	if token != "" || err == nil {
		t.Fatal("metadata error fell back")
	}
}

func TestFilesPrivateAtomicAndClear(t *testing.T) {
	files := &Files{Dir: t.TempDir() + "/accounts"}
	for _, token := range []string{"first", "second"} {
		if err := files.Write("../room", token); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(files.path("../room"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("credential mode: %v", info.Mode())
		}
		got, err := files.Read("../room")
		if err != nil || got != token {
			t.Fatal("credential did not round trip")
		}
	}
	if err := files.Write("../room", "\n"); err == nil {
		t.Fatal("accepted empty credential")
	}
	if err := files.Clear("../room"); err != nil {
		t.Fatal(err)
	}
	if err := files.Clear("../room"); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read("../room"); err == nil {
		t.Fatal("read removed credential")
	}
}

func TestValidate(t *testing.T) {
	for _, status := range []int{200, 401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer work-secret" {
					t.Error("wrong credential or route")
				}
				w.WriteHeader(status)
				if status == 200 {
					_, _ = w.Write([]byte(`{"login":"work"}`))
				} else {
					_, _ = w.Write([]byte("work-secret engine-secret"))
				}
			}))
			defer server.Close()
			login, err := Validate(context.Background(), server.Client(), server.URL, "work-secret")
			if calls != 1 {
				t.Fatalf("requests: %d", calls)
			}
			if status == 200 {
				if err != nil || login != "work" {
					t.Fatal("validation failed")
				}
			} else {
				if err == nil || login != "" || strings.Contains(err.Error(), "secret") {
					t.Fatalf("unsafe validation result: %q %v", login, err)
				}
			}
		})
	}
}

func TestValidateDoesNotRedirect(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer server.Close()
	_, err := Validate(context.Background(), server.Client(), server.URL, "secret")
	if err == nil || calls != 0 {
		t.Fatal("followed credential redirect")
	}
}
