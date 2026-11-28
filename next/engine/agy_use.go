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
	// AgyTokenIs says whether the host's token file is the same bytes as the stored file.
	AgyTokenIs(ctx context.Context, host contract.HostName, stored string) (bool, error)
	// AgyInstall copies the host's current token to takeback when that is not empty, ends the host's agy servers, puts the stored file in as
	// the host's token and compares them, and returns how many agy servers it ended.
	AgyInstall(ctx context.Context, host contract.HostName, stored, takeback string) (int, error)
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
	errAgyBusy   = errors.New("an agy seed, switch or import is already running")
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
//  1. Refuse before anything changes: an unknown account or one with no stored sign-in, nothing active to take back, a seed, switch or
//     import running, or an agy host that does not answer.
//  2. Hold: one stored flag, which the quota gate reads with the usage limit (agyLimited), so no agy turn, idle compaction or retry wake
//     goes out.
//  3. Wait up to the bound for running agy turns to end; if any remain, release and fail with their count.
//  4. Host by host, the engine host first: on a host whose token already is the account's, nothing; on the others the host's script takes
//     the outgoing sign-in back to its stored copy (the engine host only), ends the agy servers, swaps the token and compares it.
//  5. All done: the account is active and the hold is released, in one transaction. A host failed: the hold stays, and the host is named.
//
// Switching back to the old account is the way to cancel. It runs for minutes at worst, and the request has no timeout of its own.
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
	if use.Active == "" && use.Holder == "" {
		return contract.AgyUseResponse{}, invalid("no account is active yet: the sign-in on the engine host would be lost; import it first with `dyna agy import LABEL`")
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
			return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: fmt.Sprintf("host %s does not answer (%v); nothing was changed", host, err)}
		}
	}
	if err := s.service.store.BeginAgySwitch(ctx, target.ID); err != nil {
		return contract.AgyUseResponse{}, failure(err)
	}
	// The hold is set: from here the switch runs to its end or its failure even if the caller goes away.
	ctx = context.WithoutCancel(ctx)
	if left, err := s.waitAgyTurns(ctx); err != nil || left > 0 {
		s.service.store.AbortAgySwitch(ctx)
		if err != nil {
			return contract.AgyUseResponse{}, failure(err)
		}
		return contract.AgyUseResponse{}, &contract.Error{Code: contract.ErrConflict, Message: fmt.Sprintf("%d agy turns are still running; nothing was changed and agy sessions are released", left)}
	}
	var done []contract.AgyUseHost
	for _, host := range hosts {
		result, err := s.swapHost(ctx, host, target.ID, stored)
		if err != nil {
			s.service.store.StickAgySwitch(ctx, host)
			return contract.AgyUseResponse{Hosts: done}, &contract.Error{Code: contract.ErrUnavailable, Message: fmt.Sprintf("host %s: %v; the switch is not finished and agy sessions stay held; run it again, or switch back to the old account", host, err)}
		}
		done = append(done, contract.AgyUseHost{Host: host, Result: result})
	}
	if err := s.service.store.FinishAgySwitch(ctx, target.ID); err != nil {
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

// swapHost makes one host's token the stored file's, or says it already was. The engine host takes the outgoing sign-in back to the stored
// copy of the account that holds it, which a live agy may have refreshed; the others have no copy to take back.
func (s *AccountActions) swapHost(ctx context.Context, host contract.HostName, target contract.AccountID, stored string) (string, error) {
	engineHost := host == AgySource
	same, err := s.agyHosts.AgyTokenIs(ctx, host, stored)
	if err != nil {
		return "", err
	}
	if same {
		if engineHost {
			if err := s.service.store.NoteAgyHolder(ctx, target); err != nil {
				return "", err
			}
		}
		return "ok, it had the token already", nil
	}
	takeback := ""
	if engineHost {
		use, err := s.service.store.AgyUse(ctx)
		if err != nil {
			return "", err
		}
		if use.Holder != "" {
			takeback = s.service.layout.HostAgyToken(string(use.Holder))
		}
	}
	killed, err := s.agyHosts.AgyInstall(ctx, host, stored, takeback)
	if err != nil {
		return "", err
	}
	if engineHost {
		if err := s.service.store.NoteAgyHolder(ctx, target); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("ok, ended %d agy processes (work agy left running there is ended)", killed), nil
}

// AgyImport is `dyna agy import`: it takes a sign-in the owner made outside Dyna as an account the owner labels. With no file it is the
// one main holds on the engine host now, and the account becomes the active one (main holds it); a second import of main is refused while
// an account is active. With a file name in the engine host's agy accounts folder, it is that token, inactive. The token is copied whole,
// twice for main and compared (a live agy may refresh it meanwhile; one retry), and never parsed or logged: the compare is the only check,
// and the label is the owner's. The row goes in first and the file after, as for Add, under the lock Remove takes.
func (s *AccountActions) AgyImport(ctx context.Context, caller contract.Principal, req contract.AgyImportRequest) (contract.AgyImportResponse, *contract.Error) {
	if caller.Kind != contract.PrincipalOwner {
		return contract.AgyImportResponse{}, forbidden()
	}
	label, ok := model.CleanAccountLabel(req.Label)
	if !ok {
		return contract.AgyImportResponse{}, invalid("invalid account label")
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
		return contract.AgyImportResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "an account switch is unfinished; finish it, or switch back, first"}
	}
	layout := s.service.layout
	folder := layout.HostAgyTokens()
	var tmp string
	activate := false
	if req.File == "" {
		if use.Active != "" {
			return contract.AgyImportResponse{}, &contract.Error{Code: contract.ErrConflict, Message: "an account is active already, and main holds its sign-in: importing main again would add it twice"}
		}
		if tmp, err = copyStableToken(layout.HostAgyMainToken(), folder); err != nil {
			return contract.AgyImportResponse{}, &contract.Error{Code: contract.ErrUnavailable, Message: err.Error()}
		}
		activate = true
	} else {
		if req.File != filepath.Base(req.File) || req.File == "." || req.File == ".." || strings.ContainsAny(req.File, "/\\\x00") {
			return contract.AgyImportResponse{}, invalid("the file is a plain file name in the agy accounts folder, not a path")
		}
		if tmp, err = agylogin.CopyToken(filepath.Join(layout.HostAgyAccounts(), req.File), folder); err != nil {
			return contract.AgyImportResponse{}, invalid(err.Error())
		}
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
		if activate {
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

// copyStableToken copies the file twice and compares the copies, because a live agy may refresh it while it is read; one retry. It returns
// the first copy, which the caller renames into place or removes.
func copyStableToken(path, folder string) (string, error) {
	for range 2 {
		first, err := agylogin.CopyToken(path, folder)
		if err != nil {
			return "", err
		}
		second, err := agylogin.CopyToken(path, folder)
		if err != nil {
			os.Remove(first)
			return "", err
		}
		same := sameFile(first, second)
		os.Remove(second)
		if same {
			return first, nil
		}
		os.Remove(first)
	}
	return "", errors.New("the sign-in changed while it was copied, twice; try again")
}

// sameFile compares two files' bytes without keeping or showing any.
func sameFile(a, b string) bool {
	x, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	y, err := os.ReadFile(b)
	return err == nil && string(x) == string(y)
}
