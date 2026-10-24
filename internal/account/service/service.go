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
	mu        sync.Mutex // Refresh and credential renewal share one lock.
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

func (s *Service) installations() map[string]Installation {
	out := map[string]Installation{}
	for _, i := range s.installed() {
		out[i.Harness] = i
	}
	return out
}

// privateInstallations finds Dyna's private copy of each harness and the
// launch environment of its active account. The environment also carries the
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
	for _, a := range runtime.RuntimeAdapters() {
		h := a.Harness
		b, err := bundles.Resolve(h, "")
		if err != nil {
			continue
		}
		active, err := accounts.Active(h)
		if err != nil {
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
		env := slices.DeleteFunc(runtime.NativeEnvironmentBase(h, os.Environ()), func(pair string) bool {
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
		out = append(out, Installation{Harness: h, Executable: b.Executable, AccountID: active.ID, Env: env})
	}
	return out
}

// Snapshot lists Dyna's private accounts. refresh also asks each provider for
// the active account's current usage.
func (s *Service) Snapshot(ctx context.Context, refresh bool) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return s.last
	}
	accounts, err := s.nativeHomeAccounts(ctx, refresh)
	out := Snapshot{Accounts: accounts, CheckedAt: s.o.Now().UTC()}
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
