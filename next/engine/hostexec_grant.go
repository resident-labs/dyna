package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
	"strings"
	"time"
)

func (s *Service) HostexecGrant() approve.Entry {
	return approve.Entry{
		Class: func(contract.OpArgs, contract.Principal) contract.Class { return contract.ClassOwner },

		Card: func(ctx context.Context, args contract.OpArgs, _ contract.Principal) contract.Card {
			a := args.(contract.HostexecGrantArgs)
			session, _ := s.store.Session(ctx, a.To.Session)
			expiry := "for 24 hours"
			if a.Never {
				expiry = "without expiry"
			} else if !a.Expires.IsZero() {
				expiry = "until " + a.Expires.UTC().Format(time.RFC3339)
			}
			mode := "asking you before each run"
			if a.Mode == contract.GrantModePreapproved {
				mode = "without asking each time"
			}
			var hosts, commands []string
			for _, host := range a.Hosts {
				if host == contract.AllHosts {
					hosts = append(hosts, "every host, including the control plane")
				} else {
					hosts = append(hosts, string(host))
				}
			}
			for _, command := range a.Commands {
				var argv []string
				for _, arg := range append([]string{command.Program}, command.Args...) {
					safe := arg != ""
					for _, c := range arg {
						if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_./:=@%+-", c)) {
							safe = false
							break
						}
					}
					if !safe {
						arg = "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
					}
					argv = append(argv, arg)
				}
				line := strings.Join(argv, " ")
				if command.Prefix {
					line += " …"
				}
				commands = append(commands, line)
			}
			return contract.Card{Title: "Grant host execution", Target: contract.Target{Kind: contract.TargetRoom, Name: string(a.Room)}, Body: fmt.Sprintf("Let %s run %s on %s, %s, %s.", session.Name, strings.Join(commands, "\n"), strings.Join(hosts, ", "), expiry, mode)}
		},
		Check: func(ctx context.Context, args contract.OpArgs, who contract.Principal) error {
			a := args.(contract.HostexecGrantArgs)
			if !a.Expires.IsZero() && !a.Expires.After(s.now()) {
				return approve.ErrInvalid
			}
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
		Execute: func(ctx context.Context, args contract.OpArgs, _ contract.Principal) contract.OpResult {
			a := args.(contract.HostexecGrantArgs)
			now := s.now()
			value, err := id("grant_", now)
			if err != nil {
				return contract.OpResult{Error: err.Error()}
			}
			expiry := a.Expires
			if !a.Never && expiry.IsZero() {
				expiry = now.Add(24 * time.Hour)
			}
			g := contract.Grant{ID: contract.GrantID(value), Kind: contract.GrantHostExec, Room: a.Room, To: a.To, Hosts: a.Hosts, Commands: a.Commands, Mode: a.Mode, Expires: expiry, By: contract.Principal{Kind: contract.PrincipalOwner}, Created: now}
			err = s.store.Write(ctx, func(tx *sql.Tx) error { return s.store.InsertGrant(tx, g) })
			if err != nil {
				return contract.OpResult{Error: err.Error()}
			}
			return contract.OpResult{Output: value}
		},
	}
}

func (s *Service) ExpireHostexecGrants(ctx context.Context) error {
	return s.store.ExpireGrants(ctx, s.now(), func(room contract.RoomID, grant contract.GrantID) (contract.Event, error) {
		return s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.GrantExpired{Grant: grant})
	})
}
