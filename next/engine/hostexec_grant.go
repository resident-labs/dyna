package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"strconv"
	"strings"
)

func (s *Service) HostexecGrant() approve.Entry {
	return approve.Entry{
		Scope: contract.ScopeAdmin,
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassOwner },

		// The title is the phrase after the asker's name, as for every card: "<session> asks for host access".
		// The body then says "Run any command on <host> for 24 hours."
		// A single host is the target, so the web can show its name in mono.
		Card: func(ctx context.Context, args contract.OpArgs, who contract.Principal) contract.Card {
			a := args.(contract.HostexecGrantArgs)
			session, _ := s.store.Session(ctx, a.To.Session)

			var hosts []string
			warning, unknown := false, false
			records, err := s.store.Hosts(ctx)
			if err != nil {
				unknown = true
			}
			for _, name := range a.Hosts {
				if name == contract.ControlPlane || name == contract.AllHosts {
					warning = true
					break
				}
				found := false
				for _, h := range records {
					if h.Name == name {
						found = true
						if h.EngineHost == nil {
							unknown = true
						} else if *h.EngineHost {
							warning = true
						}
						break
					}
				}
				if !found {
					unknown = true
				}
			}
			for _, host := range a.Hosts {
				if host == contract.AllHosts {
					hosts = append(hosts, "every host, including the control plane")
				} else {
					hosts = append(hosts, quoteHostexec(string(host)))
				}
			}
			target := contract.Target{Kind: contract.TargetRoom, Name: string(a.Room)}
			if len(a.Hosts) == 1 && a.Hosts[0] != contract.AllHosts {
				target = contract.Target{Kind: contract.TargetHost, Name: string(a.Hosts[0])}
			}
			run := "Run"
			if who.Session != a.To.Session {
				run = "Let " + session.Name + " run"
			}
			body := fmt.Sprintf("%s any command on %s until the session closes.\nReason: %s", run, strings.Join(hosts, ", "), a.Reason)
			if warning {
				body += "\nThis host also runs the engine and holds the model sign-ins."
			}
			if unknown {
				body += "\nA host here may hold the model sign-ins. Approve only if you trust this session with them."
			}
			return contract.Card{Title: "asks for host access", Target: target, Body: body}
		},
		Check: func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
			a := args.(contract.HostexecGrantArgs)
			target, err := s.store.Session(ctx, a.To.Session)
			if err != nil || target.Room != a.Room {
				return approve.ErrInvalid
			}
			return nil
		},
		Decode: func(data json.RawMessage) (contract.OpArgs, error) {
			var a contract.HostexecGrantArgs
			err := json.Unmarshal(data, &a)
			return a, err
		},
		Execute: func(ctx context.Context, _ contract.OpID, args contract.OpArgs, _ contract.Principal) contract.OpResult {
			a := args.(contract.HostexecGrantArgs)
			now := s.now()
			value, err := id("grant_", now)
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
			}
			g := contract.Grant{ID: contract.GrantID(value), Kind: contract.GrantHostExec, Room: a.Room, To: a.To, Hosts: a.Hosts, Reason: a.Reason, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: now}
			err = s.store.CreateGrant(ctx, g)
			if err != nil {
				return contract.OpResult{Cause: contract.CauseInternal, Error: err.Error()}
			}
			return contract.OpResult{Output: value}
		},
	}
}

func quoteHostexec(value string) string {
	if value == "" {
		return strconv.Quote(value)
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_./:=@%+-", c)) {
			return strconv.Quote(value)
		}
	}
	return value
}
