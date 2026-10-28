package watch

// Get serves one PR from its own stored row and cold parts. An unrelated
// corrupt row must not fail it, a change-ID alias still resolves through the
// snapshot, and the record keeps the quota display the full read derives.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	prstate "dyna-agent/internal/pr/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

// seedRow is a stored hot body with the fields the keyed read uses.
const keyedBody = `{"record":{"roomId":"%s","host":"github.com","repo":"%s","number":%d,"state":"open","watching":true,"quotaAccount":"acct","changeId":"%s"}}`

// seedKeyedStore writes PR rows straight to the store tables, so the test
// never warms the shared snapshot first.
func seedKeyedStore(t *testing.T, rows map[string]string, index string) {
	t.Helper()
	data := store.DataDir()
	if err := prstate.Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO pr_state(id,revision,body) VALUES(1,1,?)`, index); err != nil {
		t.Fatal(err)
	}
	for key, body := range rows {
		repo, number, found := strings.Cut(strings.TrimPrefix(key, "github.com/"), "#")
		if !found {
			t.Fatalf("bad key %q", key)
		}
		n, err := strconv.Atoi(number)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO pr_rows(key,room,repo,number,closed,rev,body) VALUES(?,?,?,?,0,1,?)",
			key, "rm-x", repo, n, body); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGetReadsOnlyItsRow(t *testing.T) {
	t.Run("keyed", func(t *testing.T) {
		storetest.Isolate(t)
		pause := "2027-03-04T05:06:07Z"
		index := fmt.Sprintf(`{"version":1,"rateLimits":{"github.com\nacct\ncore":{"pauseUntil":%q,"reason":"bench quota"}}}`, pause)
		seedKeyedStore(t, map[string]string{
			"github.com/alpha/one#11": fmt.Sprintf(keyedBody, "rm-a", "alpha/one", 11, "chg_alias"),
			"github.com/beta/two#22":  fmt.Sprintf(keyedBody, "rm-b", "beta/two", 22, ""),
			// An undecodable unrelated row, and a null record, stay local to them.
			"github.com/gamma/bad#33": `{"record":"not a record"}`,
			"github.com/delta/nil#44": `{"record":null}`,
		}, index)
		data := store.DataDir()
		if err := prstate.Migrate(data); err != nil {
			t.Fatal(err)
		}
		db, err := database.Open(data)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO pr_record_parts(key,part,body) VALUES('github.com/alpha/one#11','notices','{"notifications":[{"id":"n1","text":"held"}]}')`); err != nil {
			t.Fatal(err)
		}

		s := &Store{Root: prstate.Root(data)}
		got, err := s.Get("github.com/alpha/one#11")
		if err != nil {
			t.Fatalf("Get of a good PR beside a corrupt one: %v", err)
		}
		if got.RoomID != "rm-a" || got.Number != 11 {
			t.Fatalf("Get returned %+v", got)
		}
		if got.ChangeID != "chg_alias" {
			t.Fatalf("change ID %q", got.ChangeID)
		}
		// The keyed read joins the cold notices part.
		if len(got.Notices) != 1 || got.Notices[0].ID != "n1" {
			t.Fatalf("notices %+v", got.Notices)
		}
		// The quota display is derived from the same committed index metadata
		// as the full read, not stored in the row.
		wantPause := "2027-03-04 05:06:07 +0000 UTC"
		if got.RateLimit.PauseUntil.String() != wantPause || got.RateLimit.Reason != "bench quota" {
			t.Fatalf("quota display %+v", got.RateLimit)
		}
		if _, err := s.Get("github.com/beta/two#22"); err != nil {
			t.Fatalf("Get of a second good PR: %v", err)
		}
		if _, err := s.Get("github.com/delta/nil#44"); err != prstate.ErrNotTracked {
			t.Fatalf("Get of a null record: %v, want ErrNotTracked", err)
		}
	})

	t.Run("alias", func(t *testing.T) {
		storetest.Isolate(t)
		seedKeyedStore(t, map[string]string{
			"github.com/alpha/one#11": fmt.Sprintf(keyedBody, "rm-a", "alpha/one", 11, "chg_alias"),
		}, `{"version":1}`)
		s := &Store{Root: prstate.Root(store.DataDir())}
		byAlias, err := s.Get("chg_alias")
		if err != nil || byAlias.RoomID != "rm-a" || byAlias.ChangeID != "chg_alias" {
			t.Fatalf("Get by change ID: %v %+v", err, byAlias)
		}
		if _, err := s.Get("github.com/alpha/gone#99"); err != prstate.ErrNotTracked {
			t.Fatalf("Get of a missing PR: %v, want ErrNotTracked", err)
		}
	})
}
