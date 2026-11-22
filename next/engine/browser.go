package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/browser"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func (s *Service) browserLock(name string) *sync.Mutex {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	if s.browserLocks == nil {
		s.browserLocks = map[string]*sync.Mutex{}
	}
	lock := s.browserLocks[name]
	if lock == nil {
		lock = &sync.Mutex{}
		s.browserLocks[name] = lock
	}
	return lock
}

func (s *Service) cachedBrowserJar(name string) *browser.Jar {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	if s.browserJars == nil {
		return nil
	}
	return s.browserJars[name]
}

func (s *Service) setCachedBrowserJar(name string, jar *browser.Jar) {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	if s.browserJars == nil {
		s.browserJars = map[string]*browser.Jar{}
	}
	if jar == nil {
		delete(s.browserJars, name)
		return
	}
	s.browserJars[name] = jar
}

func browserNameError(name string) *contract.Error {
	return invalid(fmt.Sprintf("invalid browser profile name %q: use 1 to 64 letters, digits, hyphens or underscores, starting with a letter or digit, and not \"none\"", name))
}

// loadBrowserJarLocked returns the profile's cookie jar, loading from disk or migrating a v1
// Chromium profile folder once if needed. Caller must hold s.browserLock(name).
func (s *Service) loadBrowserJarLocked(ctx context.Context, name string) (*browser.Jar, error) {
	if jar := s.cachedBrowserJar(name); jar != nil {
		return jar, nil
	}
	if s.layout.Data == "" {
		jar := browser.NewJar()
		s.setCachedBrowserJar(name, jar)
		return jar, nil
	}
	dir, err := s.layout.Browser(name)
	if err != nil {
		return nil, err
	}
	prevDir, err := s.layout.BrowserPrev(name)
	if err != nil {
		return nil, err
	}
	jarPath, err := s.layout.BrowserJar(name)
	if err != nil {
		return nil, err
	}
	jar, found, err := browser.LoadJar(jarPath)
	if err != nil {
		return nil, err
	}
	if found {
		_ = os.RemoveAll(prevDir)
		s.setCachedBrowserJar(name, jar)
		return jar, nil
	}
	migratedJar, migrated, err := browser.MigrateV1Profile(ctx, dir, prevDir, s.now(), s.extractV1Cookies)
	if err != nil {
		return nil, err
	}
	if migrated {
		log.Printf("browser profile %s: migrated v1 profile to cookie jar (%d cookies)", name, migratedJar.LiveCount(s.now()))
		s.setCachedBrowserJar(name, migratedJar)
		return migratedJar, nil
	}
	jar = browser.NewJar()
	s.setCachedBrowserJar(name, jar)
	return jar, nil
}

// MigrateBrowsers migrates any existing v1 browser profiles on disk into cookie jars at startup.
// Failures are logged without cookie values and leave the v1 profile intact for retry.
func (s *Service) MigrateBrowsers(ctx context.Context) {
	if s.layout.Data == "" {
		return
	}
	list, err := s.store.BrowserList(ctx)
	if err != nil {
		log.Printf("browser migration: list profiles: %v", err)
		return
	}
	for _, item := range list {
		lock := s.browserLock(item.Name)
		lock.Lock()
		_, err := s.loadBrowserJarLocked(ctx, item.Name)
		lock.Unlock()
		if err != nil {
			log.Printf("browser migration %s: %v", item.Name, err)
		}
	}
}

// BrowserCreate creates a named owner-level shared browser profile.
func (s *Service) BrowserCreate(ctx context.Context, caller contract.Principal, req contract.BrowserCreateRequest) (contract.BrowserCreateResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.BrowserCreateResponse{}, forbidden()
	}
	if !box.ValidBrowserName(req.Name) {
		return contract.BrowserCreateResponse{}, browserNameError(req.Name)
	}
	lock := s.browserLock(req.Name)
	lock.Lock()
	defer lock.Unlock()
	if err := s.store.BrowserCreate(ctx, req.Name); err != nil {
		if errors.Is(err, store.ErrBrowserExists) {
			return contract.BrowserCreateResponse{}, &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("browser profile %q already exists", req.Name)}
		}
		return contract.BrowserCreateResponse{}, failure(err)
	}
	if s.layout.Data != "" {
		dir, err := s.layout.Browser(req.Name)
		if err != nil {
			return contract.BrowserCreateResponse{}, failure(err)
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return contract.BrowserCreateResponse{}, failure(err)
		}
	}
	s.setCachedBrowserJar(req.Name, browser.NewJar())
	return contract.BrowserCreateResponse{Name: req.Name}, nil
}

// BrowserList returns every shared browser profile with its rooms and cookie counts only.
func (s *Service) BrowserList(ctx context.Context, caller contract.Principal, _ contract.BrowserListRequest) (contract.BrowserListResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.BrowserListResponse{}, forbidden()
	}
	list, err := s.store.BrowserList(ctx)
	if err != nil {
		return contract.BrowserListResponse{}, failure(err)
	}
	now := s.now()
	for i := range list {
		name := list[i].Name
		lock := s.browserLock(name)
		lock.Lock()
		jar, jerr := s.loadBrowserJarLocked(ctx, name)
		if jerr == nil && jar != nil {
			list[i].Cookies = jar.LiveCount(now)
			list[i].Version = jar.Version
		} else if jerr != nil {
			log.Printf("browser list %s: %v", name, jerr)
		}
		lock.Unlock()
	}
	return contract.BrowserListResponse{Browsers: list}, nil
}

// BrowserRemove deletes an unused shared browser profile and its files on disk.
func (s *Service) BrowserRemove(ctx context.Context, caller contract.Principal, req contract.BrowserRemoveRequest) (contract.BrowserRemoveResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.BrowserRemoveResponse{}, forbidden()
	}
	if !box.ValidBrowserName(req.Name) {
		return contract.BrowserRemoveResponse{}, browserNameError(req.Name)
	}
	lock := s.browserLock(req.Name)
	lock.Lock()
	defer lock.Unlock()
	if err := s.store.BrowserRemove(ctx, req.Name); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			return contract.BrowserRemoveResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("unknown browser profile %q", req.Name)}
		case errors.Is(err, store.ErrBrowserInUse):
			return contract.BrowserRemoveResponse{}, &contract.Error{Code: contract.ErrConflict, Message: err.Error()}
		default:
			return contract.BrowserRemoveResponse{}, failure(err)
		}
	}
	s.setCachedBrowserJar(req.Name, nil)
	if s.layout.Data != "" {
		if dir, err := s.layout.Browser(req.Name); err == nil {
			_ = os.RemoveAll(dir)
		}
		if prev, err := s.layout.BrowserPrev(req.Name); err == nil {
			_ = os.RemoveAll(prev)
		}
	}
	return contract.BrowserRemoveResponse{}, nil
}

func writeBrowserJSONError(w http.ResponseWriter, status int, code contract.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(contract.Error{Code: code, Message: message})
}

// BrowserSync serves box-only cookie jar sync (GET and POST /v1/browser/sync).
func (s *Service) BrowserSync(_ contract.Principal, room contract.RoomID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/browser/sync" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		profile, _, err := s.store.RoomBrowser(r.Context(), room)
		if err != nil {
			if errors.Is(err, store.ErrRoomNotFound) {
				writeBrowserJSONError(w, http.StatusNotFound, contract.ErrNotFound, "room not found")
				return
			}
			writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, "failed to resolve room browser profile")
			return
		}
		if profile == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var req browser.SyncRequest
		switch r.Method {
		case http.MethodGet:
			if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
				since, err := strconv.ParseInt(sinceStr, 10, 64)
				if err != nil || since < 0 {
					writeBrowserJSONError(w, http.StatusBadRequest, contract.ErrInvalid, "invalid since version")
					return
				}
				req.BaseVersion = since
			}
		case http.MethodPost:
			body := http.MaxBytesReader(w, r.Body, browser.MaxSyncRequestBytes)
			if err := json.NewDecoder(body).Decode(&req); err != nil {
				var maxErr *http.MaxBytesError
				if errors.As(err, &maxErr) {
					writeBrowserJSONError(w, http.StatusRequestEntityTooLarge, contract.ErrInvalid, fmt.Sprintf("cookie jar sync exceeds %d byte limit", maxErr.Limit))
					return
				}
				writeBrowserJSONError(w, http.StatusBadRequest, contract.ErrInvalid, "invalid cookie sync request")
				return
			}
			if err := browser.ValidateSyncRequest(&req); err != nil {
				writeBrowserJSONError(w, http.StatusBadRequest, contract.ErrInvalid, err.Error())
				return
			}
		}

		lock := s.browserLock(profile)
		lock.Lock()
		defer lock.Unlock()

		jar, err := s.loadBrowserJarLocked(r.Context(), profile)
		if err != nil {
			log.Printf("browser sync %s: load jar: %v", profile, err)
			writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, "failed to load browser cookie jar")
			return
		}

		// If the room switched to a different browser profile since the box's last sync,
		// do not apply pushes from the previous profile; send a full pull of the new profile.
		if r.Method == http.MethodPost && req.Profile != "" && req.Profile != profile {
			req = browser.SyncRequest{BaseVersion: 0}
		}

		nextJar, resp, encoded, changed, err := jar.Sync(s.now(), req)
		if err != nil {
			if errors.Is(err, browser.ErrJarFull) {
				writeBrowserJSONError(w, http.StatusRequestEntityTooLarge, contract.ErrInvalid, err.Error())
				return
			}
			log.Printf("browser sync %s: merge failed: %v", profile, err)
			writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, "failed to sync browser cookie jar")
			return
		}
		if changed && s.layout.Data != "" {
			jarPath, err := s.layout.BrowserJar(profile)
			if err != nil {
				writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, "failed to resolve browser cookie jar path")
				return
			}
			if err := browser.SaveJarBytes(jarPath, encoded); err != nil {
				log.Printf("browser sync %s: save jar: %v", profile, err)
				writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, "failed to save browser cookie jar")
				return
			}
		}
		s.setCachedBrowserJar(profile, nextJar)

		resp.Profile = profile
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

func formatRoomBrowserLine(profile string, sharedWith []string) string {
	if len(sharedWith) > 0 {
		return fmt.Sprintf("Browser: %s, shared with %s", profile, strings.Join(sharedWith, ", "))
	}
	return fmt.Sprintf("Browser: %s", profile)
}
