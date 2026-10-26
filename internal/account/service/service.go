package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/account/service/adapters"
	"dyna-agent/internal/account/store"
	harnesshome "dyna-agent/internal/harness/home"
	"dyna-agent/internal/harness/runtime"
)

// Installation is Dyna's private copy of a harness and the launch environment
// of its active account.
type Installation struct {
	Harness, Executable string
	// AccountID names the active account. Its models can differ.
	AccountID string
	Env       []string
}
type Options struct {
	Models func(context.Context, string, string, []string) ([]adapters.Model, error)
	Store  *store.Store
	Now    func() time.Time
}
type Service struct {
	// mu guards the published view: the last snapshot, the catalogs and the
	// installers. Provider I/O never runs under it.
	mu sync.Mutex
	// refreshMu serializes usage refreshes, so two callers never ask one
	// provider for the same reading at once. It is never held together with mu.
	refreshMu sync.Mutex
	o         Options
	installed func() []Installation // tests replace the private copies
	last      Snapshot
	catalogs  map[string]Catalog
}

func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Models == nil {
		o.Models = adapters.Models
	}
	s := &Service{o: o, catalogs: map[string]Catalog{}}
	s.installed = s.privateInstallations
	return s
}
func opaque(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

func (s *Service) installations() map[string][]Installation {
	out := map[string][]Installation{}
	for _, i := range s.installed() {
		out[i.Harness] = append(out[i.Harness], i)
	}
	return out
}

// privateInstallations finds Dyna's private copy of each harness and the
// launch environment of every active account. It reads the global account
// selections, so a harness that keeps one selection per provider, such as Pi,
// gets one installation for each provider. The environment also carries the
// owner tool roots a turn gets (harnesshome.ToolRoots), so a launcher script
// whose interpreter resolves through PATH, such as Pi's `env node` under a
// mise shim, still finds the owner state it trusts.
func (s *Service) privateInstallations() []Installation {
	out := []Installation{}
	if s.o.Store == nil {
		return out
	}
	accounts := &runtime.Accounts{Root: s.o.Store.Root}
	bundles := &runtime.BundleManager{Root: filepath.Join(filepath.Dir(s.o.Store.Root), "harnesses")}
	selections, err := (&store.Store{Root: s.o.Store.Root}).Selections()
	if err != nil {
		return out
	}
	for _, selection := range selections {
		if selection.Scope != "" || selection.Account == "" {
			continue
		}
		b, err := bundles.Resolve(selection.Harness, "")
		if err != nil {
			continue
		}
		active, err := accounts.Find(selection.Account)
		if err != nil || !installationRuns(active, selection) {
			continue
		}
		native, err := accounts.Env(active.ID)
		if err != nil {
			continue
		}
		tools, err := harnesshome.ToolRoots(os.Environ())
		if err != nil {
			continue
		}
		env := slices.DeleteFunc(runtime.NativeEnvironmentBase(selection.Harness, os.Environ()), func(pair string) bool {
			key, _, _ := strings.Cut(pair, "=")
			_, replaced := native[key]
			_, rooted := tools[key]
			return replaced || rooted
		})
		for key, value := range native {
			env = append(env, key+"="+value)
		}
		for key, value := range tools {
			env = append(env, key+"="+value)
		}
		out = append(out, Installation{Harness: selection.Harness, Executable: b.Executable, AccountID: active.ID, Env: env})
	}
	return out
}

// installationRuns reports whether an active account matches a global
// selection. A non-Pi harness keeps one selection key, so a keyed OpenCode
// account selected under opencode-go still matches.
func installationRuns(account runtime.NativeAccount, selection store.Selection) bool {
	return account.Harness == selection.Harness && runtime.SelectionProvider(account.Harness, account.Provider) == selection.Provider
}

// Snapshot lists Dyna's private accounts. refresh also asks each provider for
// the active account's current usage.
//
// The account service lock guards the published view, not provider I/O: a
// refresh reads the accounts and their usage without it, then publishes the new
// view under it, so one slow provider never blocks a plain listing or a model
// read. A refresh still runs one at a time, so two callers never ask a provider
// twice for the same reading.
func (s *Service) Snapshot(ctx context.Context, refresh bool) Snapshot {
	if refresh && ctx.Err() == nil {
		s.refreshMu.Lock()
		if ctx.Err() != nil {
			// The caller expired while it waited behind another refresh, so it asks
			// no provider and records nothing.
			s.refreshMu.Unlock()
			return s.cached()
		}
		accounts, problems, err := s.nativeHomeAccounts(ctx, true)
		s.refreshMu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.publish(accounts, problems, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return s.last
	}
	accounts, problems, err := s.nativeHomeAccounts(ctx, false)
	return s.publish(accounts, problems, err)
}

// cached is the last published view.
func (s *Service) cached() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// publish builds the view from one pass over the accounts and keeps it as the
// answer for later reads. The caller holds the account service lock.
func (s *Service) publish(accounts []Account, problems []runtime.AccountProblem, err error) Snapshot {
	out := Snapshot{Accounts: accounts, Problems: problems, CheckedAt: s.o.Now().UTC()}
	if err != nil {
		out.Message = err.Error()
	}
	sort.SliceStable(out.Accounts, func(i, j int) bool {
		a, b := out.Accounts[i], out.Accounts[j]
		if a.Harness != b.Harness {
			return a.Harness < b.Harness
		}
		if a.Active != b.Active {
			return a.Active
		}
		return a.ID < b.ID
	})
	ApplyBlockingQuota(out.Accounts, s.o.Now())
	s.last = out
	return out
}

// AccountUsage refreshes usage within the account API request deadline.
// Storage and provider errors stay in Snapshot.Message for the Usage view.
func (s *Service) AccountUsage(ctx context.Context, refresh bool) Snapshot {
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	return s.Snapshot(ctx, refresh)
}
