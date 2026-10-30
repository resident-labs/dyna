package librarian

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
)

// Unit tests supply the same three hook functions the engine supplies.
func configureLibrarianHooks(s *Service, observe func(string) error) {
	svc := hooks.Service{DataDir: s.DataDir}
	check := func(method string) error {
		if observe != nil {
			return observe(method)
		}
		return nil
	}
	s.ListHooks = svc.List
	s.AddHook = func(ctx context.Context, repo string, hook hooks.Hook) error {
		if err := check("AddHook"); err != nil {
			return err
		}
		hook.AddedBy = "hook-caller"
		return svc.Add(ctx, repo, hook)
	}
	s.RemoveHook = func(ctx context.Context, repo, name string) error {
		if err := check("RemoveHook"); err != nil {
			return err
		}
		return svc.Remove(ctx, repo, name)
	}
}

func TestLibraryHookChangesUseProvidedFunctions(t *testing.T) {
	h := setup(t)
	counts := map[string]int{}
	configureLibrarianHooks(&h.svc, func(method string) error { counts[method]++; return nil })
	if _, err := h.svc.Disable(t.Context(), authority.Actor{Role: authority.Owner}, h.repo); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Enable(t.Context(), authority.Actor{Role: authority.Owner}, h.repo, "room_1", "/usr/bin/dyna"); err != nil {
		t.Fatal(err)
	}
	if counts["AddHook"] != 1 || counts["RemoveHook"] != 1 {
		t.Fatalf("hook calls = %v, want 1 add and 1 remove", counts)
	}
	list, err := (hooks.Service{DataDir: h.svc.DataDir}).List(t.Context(), h.repo)
	if err != nil || len(list) != 1 {
		t.Fatalf("installed hooks = %+v, %v", list, err)
	}
	for _, hook := range list {
		if hook.AddedBy != "hook-caller" {
			t.Fatalf("hook identity lost: %+v", hook)
		}
	}
}

func TestLibraryHookFailureIsReturned(t *testing.T) {
	h := setup(t)
	configureLibrarianHooks(&h.svc, func(string) error { return errors.New("hook change failed") })
	if _, err := h.svc.Disable(t.Context(), authority.Actor{Role: authority.Owner}, h.repo); err == nil || !strings.Contains(err.Error(), "hook change failed") {
		t.Fatalf("hook error = %v", err)
	}
}
