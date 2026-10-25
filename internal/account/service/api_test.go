package service

import (
	"context"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/account/service/adapters"
)

func TestAccountAPIUsagePreservesSnapshotMessages(t *testing.T) {
	s := New(Options{})
	s.last = Snapshot{Accounts: []Account{{ID: "saved"}}, Message: "saved warning"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := s.AccountUsage(ctx, true)
	if !reflect.DeepEqual(got, s.last) {
		t.Fatalf("AccountUsage = %+v; want last snapshot", got)
	}
	cached := s.AccountUsageCached()
	if len(cached.Accounts) != 1 || cached.Accounts[0].ID != "saved" || cached.Message != "saved warning" {
		t.Fatalf("AccountUsageCached = %+v", cached)
	}
}

func TestProfileModelsPreservesEarlierRequestDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	called := false
	s := New(Options{Models: func(probe context.Context, _, _ string, _ []string) ([]adapters.Model, error) {
		called = true
		got, ok := probe.Deadline()
		if !ok || !got.Equal(want) {
			t.Fatalf("probe deadline = %v, %v; want %v", got, ok, want)
		}
		return []adapters.Model{{ID: "fixture"}}, nil
	}})
	s.installed = func() []Installation { return []Installation{{Harness: "claude-code", Executable: "/fixture"}} }
	got, err := s.ProfileModels(ctx, "claude-code", true)
	if err != nil || !called || len(got.Models) != 1 {
		t.Fatalf("ProfileModels = %+v, %v; called = %v", got, err, called)
	}
}

// Each browser entry follows the planned service API: JSON arguments after
// an optional context, with a result and an error only when needed.
// PR 2 adds context injection to websvc.Bind before direct service binding.
func TestAccountAPISignatures(t *testing.T) {
	service := reflect.TypeFor[*Service]()
	for _, name := range []string{"AccountUsage", "AccountUsageCached", "ProfileModels"} {
		method, ok := service.MethodByName(name)
		if !ok {
			t.Fatalf("missing browser method %s", name)
		}
		typ := method.Type
		wantOutputs := 1
		if name == "ProfileModels" {
			wantOutputs = 2
		}
		if typ.IsVariadic() || typ.NumOut() != wantOutputs || (wantOutputs == 2 && typ.Out(1) != reflect.TypeFor[error]()) {
			t.Fatalf("%s has an unsupported signature: %v", name, typ)
		}
		for i := 1; i < typ.NumIn(); i++ {
			arg := typ.In(i)
			if i == 1 && arg == reflect.TypeFor[context.Context]() {
				continue
			}
			if arg.Kind() != reflect.Bool && arg.Kind() != reflect.String {
				t.Fatalf("%s argument %d is not a JSON value: %v", name, i, arg)
			}
		}
	}
}
