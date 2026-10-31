package management

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// DynaMemoryOAuthCallbacks is set before the proxy starts.
var DynaMemoryOAuthCallbacks bool

// DynaOAuthCompletionHook handles a saved OAuth result before the flow returns.
var DynaOAuthCompletionHook func(state, authID string)

type dynaCallback struct {
	ch        chan oauthCallbackFilePayload
	delivered bool
}

var dynaCallbacks = struct {
	sync.Mutex
	items map[string]*dynaCallback
}{items: make(map[string]*dynaCallback)}

func dynaCallbackLocked(state string) *dynaCallback {
	slot := dynaCallbacks.items[state]
	if slot == nil {
		slot = &dynaCallback{ch: make(chan oauthCallbackFilePayload, 1)}
		dynaCallbacks.items[state] = slot
	}
	return slot
}

func dynaPrepareCallback(state string) {
	dynaCallbacks.Lock()
	defer dynaCallbacks.Unlock()
	dynaCallbackLocked(state)
}

func dynaWriteCallback(state, code, message string) error {
	dynaCallbacks.Lock()
	defer dynaCallbacks.Unlock()
	slot := dynaCallbacks.items[state]
	if slot == nil {
		return errors.New("callback unavailable")
	}
	if slot.delivered {
		return errors.New("callback already supplied")
	}
	slot.delivered = true
	slot.ch <- oauthCallbackFilePayload{State: state, Code: code, Error: message}
	return nil
}

func dynaWaitCallback(state string) (map[string]string, error) {
	dynaCallbacks.Lock()
	ch := dynaCallbackLocked(state).ch
	dynaCallbacks.Unlock()
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	select {
	case result := <-ch:
		return map[string]string{"state": result.State, "code": result.Code, "error": result.Error}, nil
	case <-timer.C:
		return nil, errors.New("callback expired")
	}
}

func dynaWaitCallbackJSON(state string) ([]byte, error) {
	result, err := dynaWaitCallback(state)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func dynaCompleteOAuth(state string, record *coreauth.Auth) {
	if !DynaMemoryOAuthCallbacks {
		CompleteOAuthSession(state)
		return
	}
	CompleteOAuthSessionWithOutcome(state, record.ID, authGrantFingerprint(record), true, false, false, 0, 0, 0, 0, "", "")
}

func dynaClearCallback(state string) {
	dynaCallbacks.Lock()
	delete(dynaCallbacks.items, state)
	dynaCallbacks.Unlock()
}
