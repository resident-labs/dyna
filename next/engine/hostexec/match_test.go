package hostexec

import (
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	now := time.Unix(100, 0)
	grant := contract.Grant{ID: "g", Kind: contract.GrantHostExec, Hosts: []contract.HostName{"host"}}
	for _, tc := range []struct {
		name  string
		grant contract.Grant
		host  contract.HostName
		want  bool
	}{
		{"host", grant, "host", true}, {"other", grant, "other", false},
		{"all", contract.Grant{ID: "g", Kind: contract.GrantHostExec, Hosts: []contract.HostName{contract.AllHosts}}, "other", true},
		{"revoked", contract.Grant{ID: "g", Kind: contract.GrantHostExec, Hosts: grant.Hosts, Revoked: now}, "host", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Match([]contract.Grant{tc.grant}, tc.host, now)
			if (err == nil) != tc.want {
				t.Fatalf("error: %v", err)
			}
		})
	}
}
