package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/agylogin"
	"github.com/resident-labs/dyna/engine/model"
	"github.com/resident-labs/dyna/engine/store"
)

// AgyHosts is what switching the active Antigravity account asks of the hosts that run agy. The token is a stored file on the engine
// host, named by its path here; it is carried as it is and never parsed, logged or hashed.
type AgyHosts interface {
	// AgyHostNames lists the agy hosts, the engine's own (AgySource) first.
	AgyHostNames(ctx context.Context) ([]contract.HostName, error)
	// AgyHostAnswers is nil when the host answers and has an account folder to put a token in.
	AgyHostAnswers(ctx context.Context, host contract.HostName) error
	// AgyInstall puts the stored file in as the host's token, under the host's lock: if the host's token already is that file it does nothing
	// and says so (same); otherwise it ends the host's agy servers (and fails if one stays), installs the file, compares, and returns how
	// many servers it ended. The host's live token is never copied out.
	AgyInstall(ctx context.Context, host contract.HostName, stored string) (killed int, same bool, err error)
}

// SetAgyHosts gives the switch its hosts.
func (a *AccountActions) SetAgyHosts(hosts AgyHosts) { a.agyHosts = hosts }

// agyOps keeps the owner's agy seed, switch and import to one at a time: a seed copies main, whose token a switch is about to replace.
type agyOps struct{ mu sync.Mutex }

// AgyExclusive starts an agy seed, switch or import, or says that another is running. end must be called when it is over.
func (s *Service) AgyExclusive() (end func(), ok bool) {
	if !s.agyOps.mu.TryLock() {
		return nil, false
	}
	return s.agyOps.mu.Unlock, true
}

// AgySwitchHeld says an account switch is in progress or was left unfinished: a seed copies nothing then.
func (s *Service) AgySwitchHeld(ctx context.Context) (bool, error) {
	use, err := s.store.AgyUse(ctx)
	return use.Held, err
}

var (
	errAgyNoHost = errors.New("no agy host is set up")
)

// agyAccountOf finds an added account by its id or its label.
func (s *AccountActions) agyAccountOf(ctx context.Context, name string) (store.AgyAccount, error) {
	accounts, err := s.service.store.AgyAccounts(ctx)
	if err != nil {
		return store.AgyAccount{}, err
	}
	var found []store.AgyAccount
	for _, account := range accounts {
		if string(account.ID) == name {
			return account, nil
		}
		if account.Label == name {
			found = append(found, account)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return store.AgyAccount{}, fmt.Errorf("no Antigravity account %q; the list is `dyna accounts`", name)
	}
	return store.AgyAccount{}, fmt.Errorf("%d Antigravity accounts are labelled %q; name one by its id", len(found), name)
}

// AgyUse is `dyna agy use`: it makes an added account the one every agy session uses. The owner only, never offered in the quota notice and
// never automatic.
//
//  1. Refuse before anything changes: an unknown account or one with no stored sign-in, no account active yet (the sign-in on the engine
//     host would be overwritten unstored), a seed, switch or import running, or an agy host that does not answer. When the switch is
//     already held, a host that does not answer is recorded as its stuck host.
//  2. Hold: one stored flag, which the quota gate reads with the usage limit (agyLimited), so no agy turn, idle compaction or retry wake
//     goes out.
//  3. Wait up to the bound for running agy turns to end; if any remain, release and fail with their count.
//  4. Host by host, the engine host first, each step bounded (agyHostWait): the host's script, under its lock, does nothing where the token
//     already is the account's, and elsewhere ends the agy servers (a failure to end one fails the host) and installs the stored
//     copy. The live token in main is never copied back: a stored copy is the sign-in as it was signed in.
//  5. All done: the account is active and the hold is released, in one transaction. A host failed: the hold stays, and the host is named.
//
// A switch left unfinished stays held and is finished by running it again for the same account; nothing else is accepted while it is held,
// and there is no cancel: finish it, then switch back. The active account changes only when every host is done. It runs for minutes at worst, and the request has no timeout of its own.
func (s *AccountActions) AgyUse(ctx context.Context, caller contract.Principal, req contract.AgyUseRequest) (contract.AgyUseResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.AgyUseResponse{}, forbidden()
	}
	if s.agyHosts == nil {
		return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: errAgyNoHost.Error()}
	}
	end, ok := s.service.AgyExclusive()
	if !ok {
		return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrConflict, Message: errAgyBusy.Error()}
	}
	defer end()
	target, err := s.agyAccountOf(ctx, strings.TrimSpace(req.Account))
	if err != nil {
		return contract.AgyUseResponse{}, invalid(err.Error())
	}
	use, err := s.service.store.AgyUse(ctx)
	if err != nil {
		return contract.AgyUseResponse{}, failure(err)
	}
	if use.Held && use.Target != target.ID {
		return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("a switch to %s is unfinished; finish the switch to %s first", s.agyLabel(ctx, use.Target), s.agyLabel(ctx, use.Target))}
	}
	if use.Active == "" {
		return contract.AgyUseResponse{}, invalid("no account is active yet: the sign-in on the engine host would be overwritten; import it first with `dyna agy import LABEL --file NAME --active`")
	}
	if use.Active == target.ID && !use.Held {
		return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("%s is the active account already", target.Label)}
	}
	stored := s.service.layout.HostAgyToken(string(target.ID))
	if info, err := os.Stat(stored); err != nil || !info.Mode().IsRegular() {
		return contract.AgyUseResponse{}, invalid(fmt.Sprintf("%s has no stored sign-in; remove it and add it again", target.Label))
	}
	hosts, err := s.agyHosts.AgyHostNames(ctx)
	if err != nil {
		return contract.AgyUseResponse{}, failure(err)
	}
	for _, host := range hosts {
		if err := s.agyHosts.AgyHostAnswers(ctx, host); err != nil {
			// A held switch names the host it cannot finish on, for the pane after a reload or a restart; a fresh one records nothing.
			if use.Held {
				s.service.store.StickAgySwitch(ctx, host)
			}
			return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: fmt.Sprintf("host %s does not answer (%v); nothing was changed", host, err)}
		}
	}
	if s.afterAgyPreflight != nil {
		s.afterAgyPreflight()
	}
	fresh, problem := s.beginAgySwitch(ctx, target.ID)
	if problem != nil {
		return contract.AgyUseResponse{}, problem
	}
	// The hold is set: from here the switch runs to its end or its failure even if the caller goes away.
	ctx = context.WithoutCancel(ctx)
	if left, err := s.waitAgyTurns(ctx); err != nil || left > 0 {
		// A hold an earlier run left stays: the engine host may already have the new token.
		if fresh {
			s.service.store.AbortAgySwitch(ctx)
		}
		if err != nil {
			return contract.AgyUseResponse{}, failure(err)
		}
		message := fmt.Sprintf("%d agy turns are still running; nothing was changed and agy sessions are released", left)
		if !fresh {
			message = fmt.Sprintf("%d agy turns are still running; the switch stays unfinished and agy sessions stay held: run it again", left)
		}
		return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrConflict, Message: message}
	}
	var done []contract.AgyUseHost
	for _, host := range hosts {
		result, err := s.swapHost(ctx, host, stored)
		if err != nil {
			s.service.store.StickAgySwitch(ctx, host)
			return contract.AgyUseResponse{Hosts: done}, &contract.Error{Code: contract.ErrUnavailable, Message: fmt.Sprintf("host %s: %v; the switch is not finished and agy sessions stay held; run the same switch again", host, err)}
		}
		done = append(done, contract.AgyUseHost{Host: host, Result: result})
	}
	if err := s.service.store.FinishAgySwitch(ctx, target.ID, time.Now()); err != nil {
		return contract.AgyUseResponse{Hosts: done}, failure(err)
	}
	return contract.AgyUseResponse{Hosts: done}, nil
}

// waitAgyTurns waits until no turn is open on an agy box, for at most agyTurnWait, and returns how many remain.
func (s *AccountActions) waitAgyTurns(ctx context.Context) (int, error) {
	wait, every := s.agyTurnWait, s.agyTurnPoll
	if wait == 0 {
		wait = 5 * time.Minute
	}
	if every == 0 {
		every = time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		n, err := s.service.store.AgyRunningTurns(ctx)
		if err != nil || n == 0 {
			return 0, err
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			return n, nil
		case <-ctx.Done():
			return n, ctx.Err()
		}
	}
}

// beginAgySwitch is the last check and the hold, under the lock Remove takes: the target is still there, row and file, and from the hold on
// Remove refuses it. It reports whether this call set the hold.
func (s *AccountActions) beginAgySwitch(ctx context.Context, target contract.AccountID) (bool, *contract.Error) {
	s.removeMu.Lock()
	defer s.removeMu.Unlock()
	if _, err := s.agyAccountOf(ctx, string(target)); err != nil {
		return false, invalid("the account was removed meanwhile; nothing was changed")
	}
	if info, err := os.Stat(s.service.layout.HostAgyToken(string(target))); err != nil || !info.Mode().IsRegular() {
		return false, invalid("the account's stored sign-in is gone; nothing was changed")
	}
	fresh, err := s.service.store.BeginAgySwitch(ctx, target)
	if err != nil {
		return false, failure(err)
	}
	return fresh, nil
}

// agyLabel is an account's label, or its id when it has none.
func (s *AccountActions) agyLabel(ctx context.Context, id contract.AccountID) string {
	if account, err := s.agyAccountOf(ctx, string(id)); err == nil {
		return account.Label
	}
	return string(id)
}

// swapHost makes one host's token the stored file's, or says it already was, within agyHostWait.
func (s *AccountActions) swapHost(ctx context.Context, host contract.HostName, stored string) (string, error) {
	wait := s.agyHostWait
	if wait == 0 {
		wait = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	killed, same, err := s.agyHosts.AgyInstall(ctx, host, stored)
	if errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("did not finish within %s", wait)
	}
	if err != nil {
		return "", err
	}
	if same {
		return "ok, it had the token already", nil
	}
	return fmt.Sprintf("ok, ended %d agy processes (work agy left running there is ended)", killed), nil
}

// AgyImport is `dyna agy import LABEL --file NAME`: it takes a sign-in the owner made outside Dyna as an account the owner labels. NAME is a
// plain name in the engine host's agy accounts folder, which boxes do not mount: a token file, or a sign-in folder (such as accounts/second)
// whose antigravity-acp/antigravity-oauth-token is taken. No component of the path is a link. The token is copied whole, never parsed or
// logged, and the label is the owner's. Dyna never reads main, the folder boxes write. With Active the account is also recorded as the one
// signed in now, on the owner's word: allowed only while no account is active. The row goes in first and the file after, as for Add, under
// the lock Remove takes.
func (s *AccountActions) AgyImport(ctx context.Context, caller contract.Principal, req contract.AgyImportRequest) (contract.AgyImportResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.AgyImportResponse{}, forbidden()
	}
	label, ok := model.CleanAccountLabel(req.Label)
	if !ok {
		return contract.AgyImportResponse{}, invalid("invalid account label")
	}
	if req.File == "" || req.File != filepath.Base(req.File) || req.File == "." || req.File == ".." || strings.ContainsAny(req.File, "/\\\x00") {
		return contract.AgyImportResponse{}, invalid("name the token file with --file: a plain file name in the agy accounts folder, not a path")
	}
	if req.File == filepath.Base(s.service.layout.HostAgyAccount()) {
		return contract.AgyImportResponse{}, invalid("main is the folder boxes write; import the sign-in folder or a copy of the token")
	}
	end, ok := s.service.AgyExclusive()
	if !ok {
		return contract.AgyImportResponse{}, &contract.Error{Code: contract.ErrConflict, Message: errAgyBusy.Error()}
	}
	defer end()
	use, err := s.service.store.AgyUse(ctx)
	if err != nil {
		return contract.AgyImportResponse{}, failure(err)
	}
	if use.Held {
		return contract.AgyImportResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "an account switch is unfinished; finish it first"}
	}
	if req.Active && use.Active != "" {
		return contract.AgyImportResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "an account is active already; --active is only for the first import"}
	}
	layout := s.service.layout
	source, err := agyImportSource(layout.HostAgyAccounts(), req.File)
	if err != nil {
		return contract.AgyImportResponse{}, invalid(err.Error())
	}
	tmp, err := agylogin.CopyToken(source, layout.HostAgyTokens())
	if err != nil {
		return contract.AgyImportResponse{}, invalid(err.Error())
	}
	defer os.Remove(tmp)
	id, err := store.NewAgyAccountID()
	if err != nil {
		return contract.AgyImportResponse{}, failure(err)
	}
	dest := layout.HostAgyToken(string(id))
	err = s.PublishAgy(ctx, id, label, func() error {
		if err := os.Rename(tmp, dest); err != nil {
			return err
		}
		if req.Active {
			if err := s.service.store.ActivateAgyAccount(ctx, id); err != nil {
				os.Remove(dest)
				return err
			}
		}
		return nil
	})
	if err != nil {
		return contract.AgyImportResponse{}, failure(err)
	}
	return contract.AgyImportResponse{Account: id}, nil
}

// agyImportSource is the token file an import of name reads: the file itself, or inside a folder of that name the account's token file.
// The name, and the two folders inside it, must not be links; the file itself is opened without following one by CopyToken.
func agyImportSource(accounts, name string) (string, error) {
	path := filepath.Join(accounts, name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("there is no token file to copy")
	}
	if info.Mode().IsRegular() {
		return path, nil
	}
	if !info.IsDir() {
		return "", errors.New("there is no token file to copy")
	}
	path = filepath.Join(path, "antigravity-acp")
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		return "", errors.New("there is no token file to copy")
	}
	return filepath.Join(path, "antigravity-oauth-token"), nil
}
