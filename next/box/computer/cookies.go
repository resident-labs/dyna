package computer

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/resident-labs/dyna/box/browser"
)

const (
	defaultCookieSyncInterval = 30 * time.Second
	cookieInitTimeout         = 10 * time.Second
	cookieTickTimeout         = 15 * time.Second
	cookieStopTimeout         = 5 * time.Second
)

// CookieStore syncs the room's shared browser cookie jar with the engine.
// Active is false when the room has no shared browser profile assigned (HTTP 204).
type CookieStore interface {
	PullCookies(ctx context.Context, since int64) (resp browser.SyncResponse, active bool, err error)
	SyncCookies(ctx context.Context, req browser.SyncRequest) (resp browser.SyncResponse, active bool, err error)
}

type cookieSession interface {
	GetCookies(ctx context.Context) ([]browser.Cookie, error)
	SetCookies(ctx context.Context, cookies []browser.Cookie) error
	DeleteCookies(ctx context.Context, keys []browser.CookieKey) error
	Close()
}

type cookieSyncer struct {
	store   CookieStore
	connect func(ctx context.Context) (cookieSession, error)
	now     func() time.Time

	mu          sync.Mutex
	initialized bool
	profile     string
	version     int64
	baseline    map[browser.KeyID]browser.Cookie
}

func newCookieSyncer(store CookieStore, connect func(context.Context) (cookieSession, error), now func() time.Time) *cookieSyncer {
	if now == nil {
		now = time.Now
	}
	return &cookieSyncer{
		store:   store,
		connect: connect,
		now:     now,
	}
}

// syncOnce performs one sync step.
// Before the first pull has been applied and read back, it only pulls (never pushes)
// and does nothing when allowInit is false (such as during stop).
func (s *cookieSyncer) syncOnce(ctx context.Context, allowInit bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		if !allowInit {
			return nil
		}
		resp, active, err := s.store.PullCookies(ctx, 0)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		sess, err := s.connect(ctx)
		if err != nil {
			return err
		}
		defer sess.Close()
		if err := sess.SetCookies(ctx, resp.Upserts); err != nil {
			return err
		}
		if err := sess.DeleteCookies(ctx, resp.Deletes); err != nil {
			return err
		}
		readBack, err := sess.GetCookies(ctx)
		if err != nil {
			return err
		}
		s.baseline = browser.CookieMap(readBack)
		s.profile = resp.Profile
		s.version = resp.Version
		s.initialized = true
		return nil
	}

	sess, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer sess.Close()

	current, err := sess.GetCookies(ctx)
	if err != nil {
		return err
	}
	upserts, deletes := browser.DiffCookies(s.baseline, current, s.now())
	resp, active, err := s.store.SyncCookies(ctx, browser.SyncRequest{
		Profile:     s.profile,
		BaseVersion: s.version,
		Upserts:     upserts,
		Deletes:     deletes,
	})
	if err != nil {
		return err
	}
	if !active {
		s.initialized = false
		s.profile = ""
		s.version = 0
		s.baseline = nil
		return nil
	}

	if resp.Profile != s.profile || len(resp.Upserts) > 0 || len(resp.Deletes) > 0 {
		if err := sess.SetCookies(ctx, resp.Upserts); err != nil {
			return err
		}
		if err := sess.DeleteCookies(ctx, resp.Deletes); err != nil {
			return err
		}
		readBack, err := sess.GetCookies(ctx)
		if err != nil {
			return err
		}
		s.baseline = browser.CookieMap(readBack)
	} else {
		s.baseline = browser.CookieMap(current)
	}
	s.profile = resp.Profile
	s.version = resp.Version
	return nil
}

func (p *Process) cookieSyncInterval() time.Duration {
	if p.syncInterval > 0 {
		return p.syncInterval
	}
	return defaultCookieSyncInterval
}

func (p *Process) cookieConnectFn() func(context.Context) (cookieSession, error) {
	if p.cookieConnect != nil {
		return p.cookieConnect
	}
	base := p.devtoolsBase()
	return func(ctx context.Context) (cookieSession, error) {
		return connect(ctx, base)
	}
}

func (p *Process) startCookieSync(ctx context.Context, browserProc *program) {
	if p.cookies == nil || browserProc == nil {
		return
	}
	// Ensure any previous loop has exited before replacing it.
	p.stopCookieSync(false)

	syncer := newCookieSyncer(p.cookies, p.cookieConnectFn(), p.now)
	syncCtx, syncCancel := context.WithCancel(context.Background())
	syncDone := make(chan struct{})

	p.mu.Lock()
	if p.closed || p.browser != browserProc {
		p.mu.Unlock()
		syncCancel()
		close(syncDone)
		return
	}
	p.syncer = syncer
	p.syncCancel = syncCancel
	p.syncDone = syncDone
	p.mu.Unlock()

	// Run the initial pull before Start returns so the first page opens with the owner's logins.
	// Cancel early if Chromium exits during startup; never fail Start on pull error.
	initCtx, initCancel := context.WithTimeout(ctx, cookieInitTimeout)
	stopWatch := make(chan struct{})
	go func() {
		select {
		case <-browserProc.done:
			initCancel()
		case <-syncCtx.Done():
			initCancel()
		case <-stopWatch:
		}
	}()
	if err := syncer.syncOnce(initCtx, true); err != nil {
		log.Printf("computer: initial cookie sync failed (will retry): %v", err)
	}
	close(stopWatch)
	initCancel()

	interval := p.cookieSyncInterval()
	go func() {
		defer close(syncDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-syncCtx.Done():
				return
			case <-browserProc.done:
				return
			case <-ticker.C:
				tickCtx, cancel := context.WithTimeout(syncCtx, cookieTickTimeout)
				if err := syncer.syncOnce(tickCtx, true); err != nil && syncCtx.Err() == nil {
					log.Printf("computer: cookie sync failed (will retry): %v", err)
				}
				cancel()
			}
		}
	}()
}

func (p *Process) stopCookieSync(finalPush bool) {
	p.mu.Lock()
	syncer := p.syncer
	cancel := p.syncCancel
	done := p.syncDone
	browserRunning := p.browser != nil
	p.syncer = nil
	p.syncCancel = nil
	p.syncDone = nil
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if finalPush && syncer != nil && browserRunning {
		finalCtx, finalCancel := context.WithTimeout(context.Background(), cookieStopTimeout)
		if err := syncer.syncOnce(finalCtx, false); err != nil {
			log.Printf("computer: final cookie sync on stop failed: %v", err)
		}
		finalCancel()
	}
}
