package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	gitroute "github.com/resident-labs/dyna/engine/git"
	"github.com/resident-labs/dyna/engine/store"
)

type pushTransport func(*http.Request) (*http.Response, error)

func (f pushTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the real store boundary through both the shared Actions push check and receive-pack.
func TestPRTeamPushRoute(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, room := range []contract.RoomID{"room", "other"} {
		if err := s.CreateRoom(ctx, room, string(room), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
		if err := s.AddRoomRepo(ctx, room, "o/r", true); err != nil {
			t.Fatal(err)
		}
	}
	err = s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO sessions(id,room,name,parent,repo,profile,state,goal,created_at) VALUES
 ('root','room','root',NULL,'o/r','p','open','',0),
 ('a','room','a','root','o/r','p','open','',0),
 ('b','room','b','root','o/r','p','open','',0),
 ('aa','room','aa','a','o/r','p','open','',0),
 ('bb','room','bb','b','o/r','p','open','',0),
 ('foreign','other','foreign',NULL,'o/r','p','open','',0)`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO boxes(id,session,image,size,state,reason,slice,created_at,updated_at) SELECT 'box-'||id,id,'','agent','ready','','',0,0 FROM sessions`)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO pr_owners(repo,number,session,goal,approved_head,head_repo,url,title,author,draft,state,head,meta,branch) VALUES
 ('o/r',1,'bb','','','o/r','u','t','x',0,'open','h','{}','bb-pr'),
 ('o/r',2,'b','','','o/r','u','t','x',0,'closed','h','{}','closed-pr'),
 ('o/r',3,'root','','','o/r','u','t','x',0,'open','h','{}','root-pr'),
 ('o/r',4,'foreign','','','o/r','u','t','x',0,'open','h','{}','foreign-pr')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	target, _ := url.Parse("https://github.com")
	for _, tt := range []struct {
		caller contract.SessionID
		branch string
		want   bool
	}{
		{"a", "bb-pr", true},    // sibling lead's descendant, shared parent root
		{"aa", "bb-pr", false},  // cousin outside caller's parent team
		{"root", "bb-pr", true}, // parentless root keeps its whole subtree
		{"aa", "root-pr", true}, // distant ancestor
		{"a", "closed-pr", false}, {"root", "closed-pr", false},
		{"root", "foreign-pr", false}, {"foreign", "bb-pr", false},
		{"missing", "bb-pr", false}, {"", "bb-pr", false},
	} {
		t.Run(string(tt.caller)+"/"+tt.branch, func(t *testing.T) {
			allowed, err := gitroute.CanPushBranch(ctx, s, "o/r", tt.caller, tt.branch)
			if err != nil || allowed != tt.want {
				t.Errorf("CanPushBranch = %v, %v; want %v", allowed, err, tt.want)
			}
			forwarded := false
			transport := pushTransport(func(*http.Request) (*http.Response, error) {
				forwarded = true
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			})
			line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/" + tt.branch + "\x00report-status\n"
			body := fmt.Sprintf("%04x%s0000PACK", len(line)+4, line)
			w := httptest.NewRecorder()
			room := contract.RoomID("room")
			if tt.caller == "foreign" {
				room = "other"
			}
			gitroute.Handler(target, transport, "secret", tt.caller, room, s).ServeHTTP(w, httptest.NewRequest("POST", "/git/o/r.git/git-receive-pack", strings.NewReader(body)))
			if forwarded != tt.want {
				t.Errorf("receive-pack forwarded = %v; want %v: %s", forwarded, tt.want, w.Body.String())
			}
			if !tt.want && tt.caller != "missing" && tt.caller != "" && !strings.Contains(w.Body.String(), "ng refs/heads/"+tt.branch) {
				t.Errorf("expected branch refusal, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
	// Push access must not grant PR lifecycle ownership.
	if owns, err := s.OwnsPR(ctx, "o/r", "a", 1); err != nil || owns {
		t.Fatalf("sibling lifecycle ownership = %v, %v", owns, err)
	}
}
