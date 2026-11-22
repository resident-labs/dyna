package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// BrowserVolumes reads and cleans shared browser profiles from stopped box volumes.
type BrowserVolumes interface {
	PullBrowserProfile(ctx context.Context, host contract.HostName, id contract.BoxID, dst io.Writer) (found bool, err error)
	RemoveBrowserProfile(ctx context.Context, host contract.HostName, id contract.BoxID) error
}

var errStaleBrowserLease = errors.New("browser lease no longer matches; checkin refused")

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

func browserNameError(name string) *contract.Error {
	return invalid(fmt.Sprintf("invalid browser profile name %q: use 1 to 64 letters, digits, hyphens or underscores, starting with a letter or digit, and not \"none\"", name))
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
	return contract.BrowserCreateResponse{Name: req.Name}, nil
}

// BrowserList returns every shared browser profile with its rooms and active lease.
func (s *Service) BrowserList(ctx context.Context, caller contract.Principal, _ contract.BrowserListRequest) (contract.BrowserListResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.BrowserListResponse{}, forbidden()
	}
	list, err := s.store.BrowserList(ctx)
	if err != nil {
		return contract.BrowserListResponse{}, failure(err)
	}
	return contract.BrowserListResponse{Browsers: list}, nil
}

// BrowserRemove deletes an unused, unleased shared browser profile and its files on disk.
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
		case errors.Is(err, store.ErrBrowserLeased), errors.Is(err, store.ErrBrowserInUse):
			return contract.BrowserRemoveResponse{}, &contract.Error{Code: contract.ErrConflict, Message: err.Error()}
		default:
			return contract.BrowserRemoveResponse{}, failure(err)
		}
	}
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

// BrowserRelease forcibly releases a stuck lease on a shared browser profile,
// discarding that box's changes and bumping the generation so a late checkin is refused.
func (s *Service) BrowserRelease(ctx context.Context, caller contract.Principal, req contract.BrowserReleaseRequest) (contract.BrowserReleaseResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.BrowserReleaseResponse{}, forbidden()
	}
	if !box.ValidBrowserName(req.Name) {
		return contract.BrowserReleaseResponse{}, browserNameError(req.Name)
	}
	lock := s.browserLock(req.Name)
	lock.Lock()
	defer lock.Unlock()
	released, gen, err := s.store.BrowserRelease(ctx, req.Name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return contract.BrowserReleaseResponse{}, &contract.Error{Code: contract.ErrNotFound, Message: fmt.Sprintf("unknown browser profile %q", req.Name)}
		}
		return contract.BrowserReleaseResponse{}, failure(err)
	}
	return contract.BrowserReleaseResponse{Released: released, Generation: gen}, nil
}

// browserLeasedMessage formats the checkout refusal naming the holding session and room
// and telling the caller how the profile frees.
func browserLeasedMessage(profile string, holder *store.BrowserLeaseRow) string {
	who := holder.SessionName
	if who == "" {
		who = string(holder.Session)
	}
	where := holder.RoomName
	if where == "" {
		where = string(holder.Room)
	}
	return fmt.Sprintf("browser profile %s is in use by %s in room %s; it frees when that session's computer stops (computer stop) or goes idle", profile, who, where)
}

func writeBrowserJSONError(w http.ResponseWriter, status int, code contract.ErrorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(contract.Error{Code: code, Message: message})
}

// BrowserSync serves box-only checkout and checkin tar streams for shared browser profiles.
func (s *Service) BrowserSync(caller contract.Principal, boxID contract.BoxID, room contract.RoomID) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/v1/browser/checkout":
			s.handleBrowserCheckout(w, r, caller, boxID, room)
		case "/v1/browser/checkin":
			s.handleBrowserCheckin(w, r, boxID)
		default:
			http.NotFound(w, r)
		}
	})
}

func (s *Service) handleBrowserCheckout(w http.ResponseWriter, r *http.Request, caller contract.Principal, boxID contract.BoxID, room contract.RoomID) {
	profile, gen, holder, err := s.store.BrowserCheckout(r.Context(), boxID, caller.Session, room, s.now())
	if errors.Is(err, store.ErrBrowserLeased) && holder != nil {
		writeBrowserJSONError(w, http.StatusConflict, contract.ErrConflict, browserLeasedMessage(profile, holder))
		return
	}
	if err != nil {
		writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, err.Error())
		return
	}
	if profile == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	dir, err := s.layout.Browser(profile)
	if err != nil {
		writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, err.Error())
		return
	}
	lock := s.browserLock(profile)
	lock.Lock()
	defer lock.Unlock()
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("X-Dyna-Browser-Profile", profile)
	w.Header().Set("X-Dyna-Browser-Generation", strconv.FormatInt(gen, 10))
	if err := browser.PackProfile(dir, w); err != nil {
		log.Printf("browser checkout %s (%s): %v", profile, boxID, err)
	}
}

func (s *Service) handleBrowserCheckin(w http.ResponseWriter, r *http.Request, boxID contract.BoxID) {
	profile := r.URL.Query().Get("profile")
	genStr := r.URL.Query().Get("generation")
	discard := r.URL.Query().Get("discard") == "1"
	gen, err := strconv.ParseInt(genStr, 10, 64)
	if !box.ValidBrowserName(profile) || err != nil || gen <= 0 {
		writeBrowserJSONError(w, http.StatusBadRequest, contract.ErrInvalid, "invalid browser profile or generation")
		return
	}
	if discard {
		lock := s.browserLock(profile)
		lock.Lock()
		defer lock.Unlock()
		ok, err := s.store.BrowserFinishCheckin(r.Context(), profile, boxID, gen)
		if err != nil {
			writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, err.Error())
			return
		}
		if !ok {
			writeBrowserJSONError(w, http.StatusConflict, contract.ErrConflict, errStaleBrowserLease.Error())
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	matches, err := s.store.BrowserLeaseMatches(r.Context(), profile, boxID, gen)
	if err != nil {
		writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, err.Error())
		return
	}
	if !matches {
		writeBrowserJSONError(w, http.StatusConflict, contract.ErrConflict, errStaleBrowserLease.Error())
		return
	}
	tmpDir, err := s.unpackBrowserTemp(profile, r.Body)
	if err != nil {
		writeBrowserJSONError(w, http.StatusBadRequest, contract.ErrInvalid, fmt.Sprintf("invalid browser profile tar: %v", err))
		return
	}
	defer os.RemoveAll(tmpDir)
	if err := s.commitBrowserCheckin(r.Context(), profile, boxID, gen, tmpDir); err != nil {
		if errors.Is(err, errStaleBrowserLease) {
			writeBrowserJSONError(w, http.StatusConflict, contract.ErrConflict, err.Error())
			return
		}
		writeBrowserJSONError(w, http.StatusInternalServerError, contract.ErrUnavailable, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Service) unpackBrowserTemp(profile string, r io.Reader) (string, error) {
	root := s.layout.Browsers()
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	tmpDir, err := os.MkdirTemp(root, ".tmp-"+profile+"-")
	if err != nil {
		return "", err
	}
	if err := browser.UnpackProfile(r, tmpDir); err != nil {
		_ = os.RemoveAll(tmpDir)
		return "", err
	}
	return tmpDir, nil
}

// commitBrowserCheckin verifies the lease generation under the profile lock, swaps
// current -> .prev and tmpDir -> current, and releases the lease.
func (s *Service) commitBrowserCheckin(ctx context.Context, profile string, boxID contract.BoxID, gen int64, tmpDir string) error {
	dir, err := s.layout.Browser(profile)
	if err != nil {
		return err
	}
	prev, err := s.layout.BrowserPrev(profile)
	if err != nil {
		return err
	}
	lock := s.browserLock(profile)
	lock.Lock()
	defer lock.Unlock()
	matches, err := s.store.BrowserLeaseMatches(ctx, profile, boxID, gen)
	if err != nil {
		return err
	}
	if !matches {
		return errStaleBrowserLease
	}
	hasCurrent := false
	if _, err := os.Stat(dir); err == nil {
		hasCurrent = true
		if err := os.RemoveAll(prev); err != nil {
			return err
		}
		if err := os.Rename(dir, prev); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmpDir, dir); err != nil {
		if hasCurrent {
			_ = os.Rename(prev, dir)
		}
		return err
	}
	ok, err := s.store.BrowserFinishCheckin(ctx, profile, boxID, gen)
	if err != nil {
		return err
	}
	if !ok {
		return errStaleBrowserLease
	}
	return nil
}

// RecoverBrowserLease recovers or releases a lease held by a box confirmed stopped or removed.
// When checkVolume is true and the box's volume still holds /box/computer-shared, it pulls
// and checks in that profile before releasing the lease and deleting computer-shared from the volume.
func (s *Service) RecoverBrowserLease(ctx context.Context, lease store.BrowserLeaseRow, host contract.HostName, checkVolume bool) error {
	release := func() error {
		lock := s.browserLock(lease.Profile)
		lock.Lock()
		defer lock.Unlock()
		_, err := s.store.BrowserFinishCheckin(ctx, lease.Profile, lease.Box, lease.Generation)
		return err
	}
	vols, hasVols := s.boxes.(BrowserVolumes)
	if !checkVolume || !hasVols || host == "" || s.layout.Data == "" {
		return release()
	}
	pr, pw := io.Pipe()
	type pullResult struct {
		found bool
		err   error
	}
	pulled := make(chan pullResult, 1)
	go func() {
		found, err := vols.PullBrowserProfile(ctx, host, lease.Box, pw)
		_ = pw.CloseWithError(err)
		pulled <- pullResult{found: found, err: err}
	}()
	tmpDir, unpackErr := s.unpackBrowserTemp(lease.Profile, pr)
	_ = pr.Close()
	res := <-pulled
	if tmpDir != "" {
		defer os.RemoveAll(tmpDir)
	}
	if res.err != nil {
		return res.err
	}
	if !res.found {
		return release()
	}
	if unpackErr != nil {
		log.Printf("browser dead-box recovery %s (%s): bad tar, releasing lease: %v", lease.Profile, lease.Box, unpackErr)
		return release()
	}
	if err := s.commitBrowserCheckin(ctx, lease.Profile, lease.Box, lease.Generation, tmpDir); err != nil && !errors.Is(err, errStaleBrowserLease) {
		return err
	}
	if err := vols.RemoveBrowserProfile(ctx, host, lease.Box); err != nil {
		log.Printf("browser dead-box cleanup %s (%s): %v", lease.Profile, lease.Box, err)
	}
	return nil
}

func formatRoomBrowserLine(profile string, sharedWith []string) string {
	if len(sharedWith) > 0 {
		return fmt.Sprintf("Browser: %s, shared with %s; one session at a time", profile, strings.Join(sharedWith, ", "))
	}
	return fmt.Sprintf("Browser: %s; one session at a time", profile)
}
