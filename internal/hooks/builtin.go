package hooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"dyna-agent/internal/authority"
)

// Builtin names one automatic hook. The name is also the hook's name, the name
// `dyna services run` takes, and the value the engine stores, so one table
// entry registers the whole hook.
type Builtin string

// BuiltinSpec registers one automatic hook: the label the screen shows, the
// schedule it starts with, and the pass it runs in the engine process.
type BuiltinSpec struct {
	Builtin      Builtin
	Label        string
	EveryMinutes int
	Timeout      string
	// Run is the pass. minutes is the hook's interval, which a pass that
	// spaces its own due times follows.
	Run func(ctx context.Context, minutes int, log io.Writer) error
}

// builtinSpec finds the table entry a stored row runs.
func (s Service) builtinSpec(name Builtin) (BuiltinSpec, bool) {
	for _, spec := range s.Builtins {
		if spec.Builtin == name {
			return spec, true
		}
	}
	return BuiltinSpec{}, false
}

// command is the by-hand run of the same pass, which the screen shows.
func (b BuiltinSpec) command() string {
	return "dyna services run " + string(b.Builtin)
}

// builtin returns the table entry a stored row runs.
func (s Service) builtin(h Hook) (BuiltinSpec, error) {
	spec, ok := s.builtinSpec(h.Builtin)
	if !ok {
		return BuiltinSpec{}, fmt.Errorf("hook %q names a built-in that this release does not have", h.Name)
	}
	if spec.Run == nil {
		return BuiltinSpec{}, errors.New("built-in hook " + string(spec.Builtin) + " has no operation")
	}
	return spec, nil
}

// Seed stores the built-in hooks of this service's table. It runs at each
// engine start and changes nothing on a second run: the interval, timeout and
// switch the owner set stay, and the fixed fields are restored. A built-in row
// that this release no longer has is removed, because its pass is gone.
func (s Service) Seed(ctx context.Context) error {
	now := time.Now().UTC()
	return s.update(ctx, globalKey, func(d *document) error {
		kept := make([]Hook, 0, len(d.Hooks)+len(s.Builtins))
		for _, h := range d.Hooks {
			if h.Builtin != "" {
				if _, known := s.builtinSpec(h.Builtin); !known {
					continue
				}
			}
			kept = append(kept, h)
		}
		// A hook the owner added before this release with a built-in's name
		// keeps running, under a free name, because the engine now owns the
		// name.
		d.Hooks = renameBuiltinNames(kept, s.Builtins)
		for _, spec := range s.Builtins {
			h := Hook{
				Name: string(spec.Builtin), Event: Schedule, Command: spec.command(),
				Scope: ScopeGlobal, Builtin: spec.Builtin, Label: spec.Label, OnFail: "alert",
				EveryMinutes: spec.EveryMinutes, Timeout: spec.Timeout,
				AddedBy: "engine", AddedAt: now,
			}
			at := -1
			for i, old := range d.Hooks {
				if old.Builtin == spec.Builtin {
					at = i
					h = old
					break
				}
			}
			h.Scope, h.Event, h.Command, h.Cwd = ScopeGlobal, Schedule, spec.command(), ""
			h.Builtin, h.Label, h.OnFail = spec.Builtin, spec.Label, "alert"
			if h.EveryMinutes < 1 {
				h.EveryMinutes = spec.EveryMinutes
			}
			if h.Timeout == "" {
				h.Timeout = spec.Timeout
			}
			if at >= 0 {
				d.Hooks[at] = h
			} else {
				d.Hooks = append(d.Hooks, h)
			}
		}
		return nil
	})
}

// Reset restores the seeded settings of a built-in hook. A hook the owner
// added has no defaults, so it has nothing to reset.
func (s Service) Reset(ctx context.Context, name, editor string) error {
	if err := authority.Check("git hooks edit", authority.Of(ctx), nil); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.update(ctx, globalKey, func(d *document) error {
		for i, h := range d.Hooks {
			if h.Name != name {
				continue
			}
			spec, ok := s.builtinSpec(h.Builtin)
			if !ok {
				return fmt.Errorf("hook %q is not built in, so it has no default to reset to", name)
			}
			h.Scope, h.Event, h.Command, h.Cwd = ScopeGlobal, Schedule, spec.command(), ""
			h.Label, h.OnFail = spec.Label, "alert"
			h.EveryMinutes, h.Timeout, h.Disabled = spec.EveryMinutes, spec.Timeout, false
			if editor == "" {
				editor = "owner"
			}
			h.EditedBy, h.EditedAt = editor, &now
			d.Hooks[i] = h
			return nil
		}
		return fmt.Errorf("unknown hook %q", name)
	})
}

// renameBuiltinNames gives a free name to a hook the owner added that holds a
// built-in's name, so seeding never stores two rows with one name. Built-in
// rows keep their own name.
func renameBuiltinNames(hooks []Hook, builtins []BuiltinSpec) []Hook {
	taken := make(map[string]bool, len(hooks))
	for _, h := range hooks {
		taken[h.Name] = true
	}
	for i, h := range hooks {
		if h.Builtin != "" {
			continue
		}
		reserved := false
		for _, spec := range builtins {
			if string(spec.Builtin) == h.Name {
				reserved = true
				break
			}
		}
		if !reserved {
			continue
		}
		name := h.Name + "-user"
		for n := 2; taken[name]; n++ {
			name = fmt.Sprintf("%s-user-%d", h.Name, n)
		}
		hooks[i].Name = name
		taken[name] = true
	}
	return hooks
}
