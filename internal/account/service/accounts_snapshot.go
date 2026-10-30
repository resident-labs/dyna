package service

import (
	"context"
	"sort"
	"time"

	"dyna-agent/internal/account/reposcope"
	"dyna-agent/internal/account/store"
	"dyna-agent/internal/harness/runtime"
)

// Scope is the place an Accounts view is taken for: a room ID and the
// identity of its repository, or the zero value for the global selection.
type Scope struct {
	Room string
	Repo string
}

// Accounts lists the accounts grouped by provider, with the account each
// provider uses in scope and whether its repo limit lets it run there. The
// zero scope is the global row. refresh asks the providers for current usage,
// as the Usage view does.
func (s *Service) Accounts(ctx context.Context, scope Scope, refresh bool) AccountsSnapshot {
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	snap := s.Snapshot(ctx, refresh)
	selections := s.selections()
	out := AccountsSnapshot{Scope: scope.Room, Providers: []ProviderAccounts{}, Problems: snap.Problems, CheckedAt: snap.CheckedAt, Message: snap.Message}
	rules := reposcope.Rules{}
	for _, a := range snap.Accounts {
		rules[a.ID] = a.Repos
	}
	groups := map[store.Provider][]ScopeAccount{}
	for _, a := range snap.Accounts {
		key := runtime.SelectionProvider(a.Harness, a.Provider)
		here := a.Active
		serves := true
		if scope.Room != "" {
			// A room with no row of its own uses the global account, as the
			// launcher does. A room row that names no account overrides it with
			// no selection.
			if id, found := selections[selectionKey{key, a.Harness, scope.Room}]; found {
				here = id == a.ID
			}
			serves = rules.Allows(a.ID, scope.Repo)
		}
		groups[a.Provider] = append(groups[a.Provider], ScopeAccount{Account: a, ActiveHere: here, ServesHere: serves})
	}
	providers := make([]store.Provider, 0, len(groups))
	for provider := range groups {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i] < providers[j] })
	for _, provider := range providers {
		rows := groups[provider]
		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].ActiveHere != rows[j].ActiveHere {
				return rows[i].ActiveHere
			}
			if rows[i].Active != rows[j].Active {
				return rows[i].Active
			}
			if rows[i].Label != rows[j].Label {
				return rows[i].Label < rows[j].Label
			}
			return rows[i].ID < rows[j].ID
		})
		out.Providers = append(out.Providers, ProviderAccounts{Provider: provider, Accounts: rows})
	}
	return out
}

// selectionKey names one row of the account selection table.
type selectionKey struct {
	provider store.Provider
	harness  string
	scope    string
}

// selections reads every saved account selection. A read failure leaves the
// active marks empty: the accounts and their usage are still worth showing.
func (s *Service) selections() map[selectionKey]string {
	out := map[selectionKey]string{}
	if s.o.Store == nil {
		return out
	}
	rows, err := (&store.Store{Root: s.o.Store.Root}).Selections()
	if err != nil {
		return out
	}
	for _, row := range rows {
		out[selectionKey{row.Provider, row.Harness, row.Scope}] = row.Account
	}
	return out
}
