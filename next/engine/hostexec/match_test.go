package hostexec

import (
	"errors"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	now := time.Unix(1000, 0)
	base := contract.Grant{ID: "g", Kind: contract.GrantHostExec, Hosts: []contract.HostName{"host"}, Commands: []contract.CommandRule{{Program: "git", Args: []string{"status"}}}, Mode: contract.GrantModeApprove}
	cases := []struct {
		name string
		g    contract.Grant
		host contract.HostName
		argv []string
		ok   bool
	}{
		{"exact", base, "host", []string{"git", "status"}, true},
		{"extra", base, "host", []string{"git", "status", "-s"}, false},
		{"shell", base, "host", []string{"sh", "-c", "git status"}, false},
		{"host", base, "other", []string{"git", "status"}, false},
	}
	leading := base
	leading.Commands = []contract.CommandRule{{Program: "git", Args: []string{"status"}, Prefix: true}}
	if _, err := Match([]contract.Grant{leading}, "host", []string{"git", "status", "-s"}, now); err != nil {
		t.Fatalf("prefix: %v", err)
	}
	prefix := base
	prefix.Commands = []contract.CommandRule{{Program: "*", Prefix: true}}
	prefix.Hosts = []contract.HostName{contract.AllHosts}
	cases = append(cases, struct {
		name string
		g    contract.Grant
		host contract.HostName
		argv []string
		ok   bool
	}{"wildcard", prefix, "other", []string{"sh", "-c", "git status"}, true})
	expired := base
	expired.Expires = now
	revoked := base
	revoked.Revoked = now
	cases = append(cases, struct {
		name string
		g    contract.Grant
		host contract.HostName
		argv []string
		ok   bool
	}{"expired", expired, "host", []string{"git", "status"}, false}, struct {
		name string
		g    contract.Grant
		host contract.HostName
		argv []string
		ok   bool
	}{"revoked", revoked, "host", []string{"git", "status"}, false})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Match([]contract.Grant{tc.g}, tc.host, tc.argv, now)
			if (err == nil) != tc.ok {
				t.Fatalf("match: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrNoGrant) {
				t.Fatal(err)
			}
		})
	}
	pre := base
	pre.ID = "pre"
	pre.Mode = contract.GrantModePreapproved
	g, err := Match([]contract.Grant{base, pre}, "host", []string{"git", "status"}, now)
	if err != nil || g.ID != "pre" || Class(g) != contract.ClassAuto || Class(base) != contract.ClassOwner {
		t.Fatalf("priority: %+v %v", g, err)
	}
}
