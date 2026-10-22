package websvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"dyna-agent/internal/callspec"
)

// Call runs one API method with the request context and positional JSON arguments.
type Call func(context.Context, []json.RawMessage) (any, error)

// Access states whether a bound method can change stored state.
// Zero is invalid, so each API entry must make an explicit choice.
type Access uint8

const (
	Read Access = iota + 1
	Write
)

// Scope is the one authorization class of a bound method.
// A scoped device may call the method only when it holds this scope.
// The zero value is invalid, so an untagged method is rejected at startup.
type Scope string

const (
	// ScopeRead views rooms, transcripts, pull requests, goals, and runs.
	ScopeRead Scope = "read"
	// ScopeOperate posts, answers, stops work, and controls sessions and runs that already exist. It does not start a script.
	ScopeOperate Scope = "operate"
	// ScopeFiles reads the workspace, git contents, and browse paths.
	ScopeFiles Scope = "files"
	// ScopeTerminal opens a shell and types into it.
	ScopeTerminal Scope = "terminal"
	// ScopeComputers lists computers and controls their screens.
	ScopeComputers Scope = "computers"
	// ScopeAdmin changes settings, accounts, updates, pairing, and hooks, and starts a workflow file.
	ScopeAdmin Scope = "admin"
)

func ValidScope(s Scope) bool {
	switch s {
	case ScopeRead, ScopeOperate, ScopeFiles, ScopeTerminal, ScopeComputers, ScopeAdmin:
		return true
	default:
		return false
	}
}

// Binding gives a function the name used by the web client.
type Binding struct {
	Name   string
	Method any
	Access Access
	Scope  Scope
	// Local keeps the call to the engine's private socket: a paired device or
	// a Connect user is refused whatever its scopes. A call that deletes or
	// forces something, or that only a local process can make, sets it.
	Local bool
}

var (
	errType     = reflect.TypeFor[error]()
	contextType = reflect.TypeFor[context.Context]()
)

// Bind validates an explicit API table before the server starts. Functions may
// take context.Context as their first parameter; it is supplied by the server,
// not by a JSON argument. Functions return a result, an error, both, or neither.
func Bind(table []Binding) (map[string]Call, error) {
	calls := make(map[string]Call, len(table))
	for _, binding := range table {
		if binding.Access != Read && binding.Access != Write {
			return nil, fmt.Errorf("websvc: %s must declare read or write access", binding.Name)
		}
		name := binding.Name
		if name == "" {
			return nil, fmt.Errorf("websvc: binding name is empty")
		}
		if _, exists := calls[name]; exists {
			return nil, fmt.Errorf("websvc: duplicate binding %s", name)
		}
		m := reflect.ValueOf(binding.Method)
		if !m.IsValid() || m.Kind() != reflect.Func || m.IsNil() {
			return nil, fmt.Errorf("websvc: %s must bind a non-nil function", name)
		}
		t := m.Type()
		if t.IsVariadic() || t.NumOut() > 2 || (t.NumOut() == 2 && t.Out(1) != errType) {
			return nil, fmt.Errorf("websvc: %s has a shape the web API does not support", name)
		}
		for i := 1; i < t.NumIn(); i++ {
			if t.In(i) == contextType {
				return nil, fmt.Errorf("websvc: %s must take context.Context as its first parameter", name)
			}
		}
		if !ValidScope(binding.Scope) {
			return nil, fmt.Errorf("websvc: %s must declare a valid scope", name)
		}
		calls[name] = bindOne(name, m, t)
		if binding.Local {
			calls[name] = localOnly(name, calls[name])
		}
	}
	return calls, nil
}

// deadline is each call's deadline, the one the client waits for. Tests
// shorten it.
var deadline = callspec.Deadline

// localOnly refuses a caller that is not on the engine's private socket.
func localOnly(name string, call Call) Call {
	return func(ctx context.Context, args []json.RawMessage) (any, error) {
		if _, local := CallerFromContext(ctx); !local {
			return nil, fmt.Errorf("%s is available only through the local engine socket", name)
		}
		return call(ctx, args)
	}
}

// bindOne gives each call its deadline.
func bindOne(name string, m reflect.Value, t reflect.Type) Call {
	timeout := deadline(name)
	offset := 0
	if t.NumIn() > 0 && t.In(0) == contextType {
		offset = 1
	}
	return func(ctx context.Context, raw []json.RawMessage) (any, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		argc := t.NumIn() - offset
		if len(raw) != argc {
			return nil, fmt.Errorf("%s takes %d arguments, got %d", name, argc, len(raw))
		}
		in := make([]reflect.Value, t.NumIn())
		if offset == 1 {
			in[0] = reflect.ValueOf(ctx)
		}
		for i, r := range raw {
			p := reflect.New(t.In(i + offset))
			if err := json.Unmarshal(r, p.Interface()); err != nil {
				return nil, fmt.Errorf("%s argument %d: %v", name, i+1, err)
			}
			in[i+offset] = p.Elem()
		}
		res, err := results(t, m.Call(in))
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return res, callspec.Late(name, timeout)
		}
		return res, err
	}
}

func results(t reflect.Type, out []reflect.Value) (any, error) {
	switch len(out) {
	case 0:
		return nil, nil
	case 1:
		if t.Out(0) == errType {
			return nil, asErr(out[0])
		}
		return out[0].Interface(), nil
	}
	return out[0].Interface(), asErr(out[1])
}

func asErr(v reflect.Value) error {
	if v.IsNil() {
		return nil
	}
	err, _ := v.Interface().(error)
	if err == nil {
		return errors.New("unknown error")
	}
	return err
}
