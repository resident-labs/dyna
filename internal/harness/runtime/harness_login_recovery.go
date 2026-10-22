package runtime

import (
	"errors"
	"sort"

	"dyna-agent/internal/terminalsvc"
)

// PrivateRuntimeLogins lets a remounted client recover each native terminal.
// Only public identifiers and warnings leave the desktop process.
func (a *Runtime) PrivateRuntimeLogins() []PrivateLogin {
	a.privateLoginMu.Lock()
	defer a.privateLoginMu.Unlock()
	out := []PrivateLogin{}
	for id, login := range a.privateLogins {
		out = append(out, PrivateLogin{AccountID: id, TerminalID: login.terminalID, Warning: login.flow.Warning})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// CancelPrivateRuntimeLogin stops native work before releasing its login gate.
// It keeps the saved account slot, so the user can start sign-in again.
func (a *Runtime) CancelPrivateRuntimeLogin(id string) error {
	return a.cancelPrivateLogin(id)
}

func (a *Runtime) cancelPrivateLogin(id string) error {
	a.privateLoginMu.Lock()
	login := a.privateLogins[id]
	a.privateLoginMu.Unlock()
	if login == nil {
		return nil
	}
	login.control.Lock()
	defer login.control.Unlock()
	flow, terminalID := login.flow, login.terminalID
	if a.terminals != nil {
		for _, terminal := range a.terminals.List("") {
			if terminal.ID == terminalID {
				if err := a.terminals.Close(terminalID); err != nil && !errors.Is(err, terminalsvc.ErrNotFound) {
					return err
				}
				break
			}
		}
	}
	err := flow.Cancel()
	login.finished = true
	a.privateLoginMu.Lock()
	if a.privateLogins[id] == login {
		delete(a.privateLogins, id)
	}
	a.privateLoginMu.Unlock()
	return err
}
