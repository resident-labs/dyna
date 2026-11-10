package store

import (
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestActiveGrantsExcludeElapsedExpiry(t *testing.T) {
	s, ctx := branchStore(t)
	now := time.Unix(1000, 0)
	who := contract.Principal{Kind: contract.PrincipalSession, Session: "a"}
	for _, test := range []struct {
		id      contract.GrantID
		expires time.Time
	}{
		{"expired", now.Add(-time.Millisecond)}, {"boundary", now}, {"future", now.Add(time.Millisecond)}, {"unlimited", time.Time{}},
	} {
		if err := s.CreateGrant(ctx, contract.Grant{ID: test.id, Kind: contract.GrantHostExec, Room: "room", To: who, Hosts: []contract.HostName{"host"}, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: now, Expires: test.expires}); err != nil {
			t.Fatal(err)
		}
	}
	grants, err := s.ListActiveGrants(ctx, "room", who, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 || grants[0].ID != "future" || grants[1].ID != "unlimited" {
		t.Fatalf("active grants %+v", grants)
	}
	all, err := s.ListHostexecGrants(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("listed grants %+v", all)
	}
}
