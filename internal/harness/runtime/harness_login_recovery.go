package runtime

import "sort"

// PrivateRuntimeLogins lets a remounted client recover each sign-in card.
// Only public identifiers and warnings leave the desktop process.
func (a *Runtime) PrivateRuntimeLogins() []PrivateLogin {
	a.privateLoginMu.Lock()
	defer a.privateLoginMu.Unlock()
	out := []PrivateLogin{}
	for id, login := range a.privateLogins {
		state := login.signIn.snapshot()
		row := PrivateLogin{AccountID: id, SignIn: &state}
		if login.flow != nil {
			row.Warning = login.flow.Warning
		}
		out = append(out, row)
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
	// Stop and join native work before restoring credentials or releasing the gate.
	login.signIn.set(func(st *SignInState) { st.Phase = signInCancelled })
	login.signIn.stop()
	<-login.signIn.done
	var err error
	if login.flow != nil {
		err = login.flow.Cancel()
	}
	a.privateLoginMu.Lock()
	if a.privateLogins[id] == login {
		delete(a.privateLogins, id)
	}
	a.privateLoginMu.Unlock()
	return err
}
