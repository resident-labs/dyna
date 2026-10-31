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
		Card: func(args contract.OpArgs, _ contract.Principal) contract.Card {
			a := args.(contract.HostexecGrantArgs)
			expiry := "24 hours from approval"
			if !a.Expires.IsZero() {
				expiry = a.Expires.UTC().Format(time.RFC3339)
			}
			if a.Never {
				expiry = "never"
			}
			mode := "approve (each run asks the owner)"
			if a.Mode == contract.GrantModePreapproved {
				mode = "preapproved (runs without asking)"
			}
			var lines []string
			for _, c := range a.Commands {
				lines = append(lines, fmt.Sprintf("%q (prefix: %t)", append([]string{c.Program}, c.Args...), c.Prefix))
			}
			return contract.Card{Title: "Grant host execution", Target: contract.Target{Kind: contract.TargetRoom, Name: string(a.Room)}, Body: fmt.Sprintf("Session: %s\nHosts: %v\nCommands: %s\nMode: %s\nExpiry: %s", a.To.Session, a.Hosts, strings.Join(lines, ", "), mode, expiry)}
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
