package websvc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/scope"
	"dyna-agent/internal/websvc/devicestore"
)

func TestBindRejectsInvalidTable(t *testing.T) {
	var nilFunc func()
	for _, tc := range []struct {
		name  string
		table []Binding
		want  string
	}{
		{"missing access", []Binding{{Name: "Unset", Method: func() {}}}, "must declare read or write access"},
		{"unknown access", []Binding{{Name: "Unknown", Method: func() {}, Access: 99}}, "must declare read or write access"},
		{"duplicate", []Binding{{Name: "Same", Access: Read, Scope: ScopeRead, Method: func() {}}, {Name: "Same", Access: Read, Scope: ScopeRead, Method: func() {}}}, "duplicate binding Same"},
		{"empty name", []Binding{{Name: "", Access: Read, Method: func() {}}}, "name is empty"},
		{"nil", []Binding{{Name: "Bad", Access: Read, Method: nil}}, "non-nil function"},
		{"typed nil", []Binding{{Name: "Bad", Access: Read, Method: nilFunc}}, "non-nil function"},
		{"non-function", []Binding{{Name: "Bad", Access: Read, Method: 1}}, "non-nil function"},
		{"variadic", []Binding{{Name: "Bad", Access: Read, Method: func(...string) {}}}, "shape"},
		{"wrong second result", []Binding{{Name: "Bad", Access: Read, Method: (&fakeAPI{}).Both}}, "shape"},
		{"too many results", []Binding{{Name: "Bad", Access: Read, Method: func() (int, int, error) { return 0, 0, nil }}}, "shape"},
		{"misplaced context", []Binding{{Name: "Bad", Access: Read, Method: func(string, context.Context) {}}}, "first parameter"},
		{"second context", []Binding{{Name: "Bad", Access: Read, Method: func(context.Context, context.Context) {}}}, "first parameter"},
		{"missing scope", []Binding{{Name: "Unscoped", Access: Read, Method: func() {}}}, "Unscoped must declare a valid scope"},
		{"unknown scope", []Binding{{Name: "BadScope", Access: Read, Scope: "nope", Method: func() {}}}, "BadScope must declare a valid scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := Bind(tc.table)
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != nil {
				t.Fatalf("Bind = %v, %v; want %q", calls, err, tc.want)
			}
		})
	}
}

type callerKey struct{}

// The method gets the caller's context with the call's deadline added.
func TestBindContextAndArguments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), callerKey{}, "caller"))
	defer cancel()
	calls, err := Bind([]Binding{{Name: "BrowserName", Access: Read, Scope: ScopeRead, Method: func(got context.Context, s string, n int) (string, error) {
		if got.Value(callerKey{}) != "caller" {
			t.Fatal("method did not receive the caller's context")
		}
		if deadline, ok := got.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			t.Fatalf("method context deadline = %v, %v; want at most 5 s", deadline, ok)
		}
		return strings.Repeat(s, n), got.Err()
	}}})
	if err != nil {
		t.Fatal(err)
	}
	call := calls["BrowserName"]
	args := []json.RawMessage{json.RawMessage(`"a"`), json.RawMessage(`2`)}
	if got, err := call(ctx, args); got != "aa" || err != nil {
		t.Fatalf("call = %v, %v", got, err)
	}
	if _, err := call(ctx, args[:1]); err == nil || !strings.Contains(err.Error(), "takes 2 arguments, got 1") {
		t.Fatalf("arity error = %v", err)
	}
	if _, err := call(ctx, []json.RawMessage{json.RawMessage(`1`), args[1]}); err == nil || !strings.Contains(err.Error(), "argument 1") {
		t.Fatalf("decode error = %v", err)
	}
	cancel()
	if _, err := call(ctx, args); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestBindResults(t *testing.T) {
	wantErr := errors.New("failed")
	called := false
	for _, tc := range []struct {
		name   string
		method any
		want   any
		err    error
	}{
		{"none", func() { called = true }, nil, nil},
		{"value", func() string { return "result" }, "result", nil},
		{"nil error", func() error { return nil }, nil, nil},
		{"error", func() error { return wantErr }, nil, wantErr},
		{"both", func() (int, error) { return 3, nil }, 3, nil},
		{"both error", func() (int, error) { return 4, wantErr }, 4, wantErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, err := Bind([]Binding{{Name: "Call", Access: Read, Scope: ScopeRead, Method: tc.method}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := calls["Call"](context.Background(), nil)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("call = %v, %v; want %v, %v", got, err, tc.want, tc.err)
			}
		})
	}
	if !called {
		t.Fatal("function with no results did not run")
	}
}

func TestAPIRequestPrincipalAndContext(t *testing.T) {
	for _, kind := range []string{"device", "connect"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			tok := e.paired()
			dev, ok := e.store.Check(tok)
			if !ok {
				t.Fatal("paired device missing")
			}
			want := Principal{Kind: "device", ID: dev.ID, Device: dev.ID, Name: dev.Name, Scopes: scope.All()}
			if kind == "connect" {
				tok = ""
				want = Principal{Kind: "connect", ID: "cs_test", Scopes: scope.Default()}
				e.srv.o.Ext = terminalExtension{result: AuthOK}
				e.putGrant("connect", "cs_test", scope.Default())
			}
			if p, ok := PrincipalFromContext(context.Background()); ok || !reflect.DeepEqual(p, Principal{}) {
				t.Fatalf("unauthenticated principal = %+v, %v", p, ok)
			}
			type requestKey struct{}
			ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), requestKey{}, "request"), time.Now().Add(time.Second))
			defer cancel()
			called := false
			calls, err := Bind([]Binding{{Name: "Actor", Access: Read, Scope: ScopeRead, Method: func(got context.Context) string {
				called = true
				p, ok := PrincipalFromContext(got)
				if !ok || !reflect.DeepEqual(p, want) {
					t.Errorf("principal = %+v, %v; want %+v", p, ok, want)
				}
				if got.Value(requestKey{}) != "request" {
					t.Error("request value missing")
				}
				if deadline, ok := got.Deadline(); !ok {
					t.Error("request deadline missing")
				} else if want, _ := ctx.Deadline(); deadline != want {
					t.Error("request deadline changed")
				}
				cancel()
				if !errors.Is(got.Err(), context.Canceled) {
					t.Error("request cancellation missing")
				}
				return p.Name
			}}})
			if err != nil {
				t.Fatal(err)
			}
			e.srv.o.Calls = calls
			e.srv.o.CallScopes = map[string]Scope{"Actor": ScopeRead}
			r := httptest.NewRequest(http.MethodPost, tsApp+"/api/Actor", strings.NewReader(`[]`)).WithContext(ctx)
			r.Header.Set("Origin", tsApp)
			r.Header.Set("X-Dyna", "1")
			r.Header.Set("Content-Type", "application/json")
			if tok != "" {
				r.AddCookie(&http.Cookie{Name: devicestore.CookieName, Value: tok})
			}
			w := httptest.NewRecorder()
			e.app.ServeHTTP(w, r)
			if w.Code != http.StatusOK || !called {
				t.Fatalf("call = %d %s; called = %v", w.Code, w.Body, called)
			}
		})
	}
}

// A method stopped by the call deadline reports the same line as the client,
// never a bare "context deadline exceeded".
func TestCallPastItsDeadlineSaysEngineDidNotAnswer(t *testing.T) {
	old := deadline
	deadline = func(string) time.Duration { return 10 * time.Millisecond }
	t.Cleanup(func() { deadline = old })
	calls, err := Bind([]Binding{{Name: "Slow", Access: Read, Scope: ScopeRead, Method: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calls["Slow"](context.Background(), nil); err == nil || err.Error() != "engine did not answer Slow in 0.01 s" {
		t.Fatalf("late call = %v", err)
	}
}

// A Local binding is the one place that keeps a call on the engine's private
// socket: a paired device or a Connect user is refused before the method runs.
func TestLocalBindingRefusesRemoteCallers(t *testing.T) {
	ran := 0
	calls, err := Bind([]Binding{{Name: "Forget", Access: Write, Scope: ScopeAdmin, Local: true, Method: func() { ran++ }}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := calls["Forget"](context.Background(), nil); err == nil || err.Error() != "Forget is available only through the local engine socket" || ran != 0 {
		t.Fatalf("a remote call = %v after %d runs; want the refusal and no run", err, ran)
	}
	local := context.WithValue(context.Background(), callerContextKey{}, identified{caller: Caller{Kind: Owner}})
	if _, err := calls["Forget"](local, nil); err != nil || ran != 1 {
		t.Fatalf("a local call = %v after %d runs; want one run", err, ran)
	}
}
